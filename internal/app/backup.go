package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

const MaximumBackupBytes = 64 * 1024

var ErrInvalidBackup = errors.New("invalid metadata backup")
var ErrBackupFile = errors.New("metadata backup file operation failed")

type MetadataBackup struct {
	Version     int                       `json:"version"`
	Environment string                    `json:"environment"`
	TestDC      int                       `json:"test_dc"`
	Retention   store.AuditRetention      `json:"retention"`
	Scopes      []policy.RecoverableScope `json:"scopes"`
}

func (b MetadataBackup) validate() error {
	if b.Version != 1 || !((b.Environment == store.TestEnvironment && b.TestDC >= 1 && b.TestDC <= 3) || (b.Environment == store.ProductionEnvironment && b.TestDC == 0)) {
		return ErrInvalidBackup
	}
	if err := b.Retention.Validate(); err != nil {
		return errors.Join(ErrInvalidBackup, err)
	}
	if err := policy.ValidateRecoveryScopes(b.Scopes); err != nil {
		return errors.Join(ErrInvalidBackup, err)
	}
	return nil
}

// Reject ambiguous JSON before the typed decoder, including nested duplicates,
// nulls and case aliases. Limits also bound recursive parser work.
func backupValue(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrInvalidBackup
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidBackup
	}
	if token == nil {
		return ErrInvalidBackup
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return ErrInvalidBackup
			}
			key, ok := token.(string)
			if !ok || seen[key] || key != strings.ToLower(key) {
				return ErrInvalidBackup
			}
			seen[key] = true
			if err := backupValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := backupValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidBackup
	}
	_, err = decoder.Token()
	if err != nil {
		return ErrInvalidBackup
	}
	return nil
}

func decodeBackup(data []byte) (MetadataBackup, error) {
	var b MetadataBackup
	if len(data) > MaximumBackupBytes || !utf8.Valid(data) {
		return b, ErrInvalidBackup
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := backupValue(decoder, 0); err != nil {
		return b, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return b, ErrInvalidBackup
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return MetadataBackup{}, ErrInvalidBackup
	}
	if err := b.validate(); err != nil {
		return MetadataBackup{}, err
	}
	// Every field is required, including the zero-valued production test DC.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 5 {
		return MetadataBackup{}, ErrInvalidBackup
	}
	return b, nil
}

func InspectBackup(path string) (MetadataBackup, error) {
	data, err := privatefs.ReadFile(path, MaximumBackupBytes)
	if err != nil {
		return MetadataBackup{}, errors.Join(ErrBackupFile, err)
	}
	return decodeBackup(data)
}

func writeBackup(path string, backup MetadataBackup) error {
	if err := backup.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return ErrInvalidBackup
	}
	if len(data) > MaximumBackupBytes {
		return ErrInvalidBackup
	}
	if err := privatefs.CheckDirectory(filepath.Dir(path)); err != nil {
		return errors.Join(ErrBackupFile, err)
	}
	if err := privatefs.WriteFile(path, data, false); err != nil {
		return errors.Join(ErrBackupFile, err)
	}
	return nil
}
