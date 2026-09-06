// Package secrets stores only local account credentials, sessions and integrity
// keys. Its unencrypted file backend relies on OS account permissions.
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

const MaximumSecretBytes = 1024 * 1024
const maximumFileBytes = 6 * 1024 * 1024

var ErrNotFound = errors.New("secret not found")
var ErrInvalidStore = errors.New("local secret store is invalid")
var ErrStoreExists = errors.New("local secret store already exists")

type FileStore struct{ directory string }

type fileData struct {
	Version           int    `json:"version"`
	Credentials       []byte `json:"credentials,omitempty"`
	TestSession       []byte `json:"test_session,omitempty"`
	ProductionSession []byte `json:"production_session,omitempty"`
	IntegrityKey      []byte `json:"integrity_key,omitempty"`
}

func NewFileStore(directory string) (*FileStore, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalidStore
	}
	return &FileStore{directory: directory}, nil
}

func (d *fileData) clear() {
	clear(d.Credentials)
	clear(d.TestSession)
	clear(d.ProductionSession)
	clear(d.IntegrityKey)
}
func (d *fileData) item(account string) (*[]byte, error) {
	switch account {
	case "default.credentials":
		return &d.Credentials, nil
	case "default.session":
		return &d.TestSession, nil
	case "production.session":
		return &d.ProductionSession, nil
	case "default.cursor-integrity":
		return &d.IntegrityKey, nil
	default:
		return nil, ErrInvalidStore
	}
}
func (d *fileData) validate() error {
	if d.Version != 1 || len(d.Credentials) > MaximumSecretBytes || len(d.TestSession) > MaximumSecretBytes || len(d.ProductionSession) > MaximumSecretBytes || (len(d.IntegrityKey) != 0 && len(d.IntegrityKey) != 32) {
		return ErrInvalidStore
	}
	return nil
}

// Only fixed field names and canonical JSON emitted by this store are accepted.
// A byte comparison also rejects duplicate keys, case aliases and unknown fields.
func decodeFile(data []byte) (fileData, error) {
	var d fileData
	if err := json.Unmarshal(data, &d); err != nil {
		d.clear()
		return fileData{}, ErrInvalidStore
	}
	if err := d.validate(); err != nil {
		d.clear()
		return fileData{}, err
	}
	canonical, err := json.Marshal(d)
	if err != nil {
		d.clear()
		return fileData{}, ErrInvalidStore
	}
	defer clear(canonical)
	if !bytes.Equal(canonical, data) {
		d.clear()
		return fileData{}, ErrInvalidStore
	}
	return d, nil
}

func (s *FileStore) withLock(ctx context.Context, operation func() error) (resultError error) {
	if s == nil || ctx == nil {
		return ErrInvalidStore
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := privatefs.EnsureDirectory(s.directory); err != nil {
		return err
	}
	lock, err := daemon.AcquireAccountLock(filepath.Join(s.directory, "secrets.lock"))
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, lock.Release()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}
func (s *FileStore) load() (fileData, error) {
	data, err := privatefs.ReadFile(filepath.Join(s.directory, "secrets.json"), maximumFileBytes)
	defer clear(data)
	if errors.Is(err, os.ErrNotExist) {
		return fileData{}, ErrNotFound
	}
	if err != nil {
		return fileData{}, err
	}
	return decodeFile(data)
}
func (s *FileStore) save(d fileData, replace bool) error {
	if err := d.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return ErrInvalidStore
	}
	defer clear(data)
	return privatefs.WriteFile(filepath.Join(s.directory, "secrets.json"), data, replace)
}
func (s *FileStore) Get(ctx context.Context, account string) (value []byte, resultError error) {
	resultError = s.withLock(ctx, func() error {
		d, err := s.load()
		if err != nil {
			return err
		}
		defer d.clear()
		item, err := d.item(account)
		if err != nil {
			return err
		}
		if len(*item) == 0 {
			return ErrNotFound
		}
		value = append([]byte(nil), (*item)...)
		return nil
	})
	if resultError != nil {
		clear(value)
		return nil, resultError
	}
	return value, nil
}
func (s *FileStore) Put(ctx context.Context, account string, secret []byte) error {
	if len(secret) == 0 || len(secret) > MaximumSecretBytes {
		return ErrInvalidStore
	}
	return s.withLock(ctx, func() error {
		d, err := s.load()
		replace := true
		if errors.Is(err, ErrNotFound) {
			if account != "default.credentials" {
				return ErrNotFound
			}
			d = fileData{Version: 1}
			replace = false
		} else if err != nil {
			return err
		}
		defer d.clear()
		item, err := d.item(account)
		if err != nil {
			return err
		}
		clear(*item)
		*item = append([]byte(nil), secret...)
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.save(d, replace)
	})
}
func (s *FileStore) Delete(ctx context.Context, account string) error {
	return s.withLock(ctx, func() error {
		d, err := s.load()
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		defer d.clear()
		item, err := d.item(account)
		if err != nil {
			return err
		}
		clear(*item)
		*item = nil
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.save(d, true)
	})
}

// Import publishes a complete legacy migration in one write. It never merges
// with or replaces an existing file, including an empty initialized store.
func (s *FileStore) Import(ctx context.Context, values map[string][]byte) error {
	d := fileData{Version: 1}
	defer d.clear()
	for account, value := range values {
		item, err := d.item(account)
		if err != nil {
			return err
		}
		if len(value) == 0 {
			return ErrInvalidStore
		}
		*item = append([]byte(nil), value...)
	}
	if len(d.Credentials) == 0 {
		return ErrInvalidStore
	}
	return s.withLock(ctx, func() error {
		if _, err := os.Lstat(filepath.Join(s.directory, "secrets.json")); err == nil {
			return ErrStoreExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return s.save(d, false)
	})
}
