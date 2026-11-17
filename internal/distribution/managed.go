package distribution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

const EntryProtocol = 1

var (
	ErrEntryMigrationRequired = errors.New("managed entry migration is required; close clients and run the new control program directly")
	ErrEntryInvalid           = errors.New("managed entry capability is invalid")
)

// EntryInfo identifies the physically executing entry, independent of service selection.
type EntryInfo struct {
	Protocol   int    `json:"entry_protocol"`
	Executable string `json:"executable"`
}

// ManagedVersion recognizes only the exact canonical layout owned by this installer.
func ManagedVersion(root, directory string) string {
	version := filepath.Base(directory)
	if ValidVersion(version) && directory == filepath.Join(root, "versions", version) {
		return version
	}
	return ""
}

// CompareVersions compares validated stable versions without integer overflow.
func CompareVersions(a, b string) (int, error) {
	if !ValidVersion(a) || !ValidVersion(b) {
		return 0, errors.New("invalid release version")
	}
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for n := range left {
		if len(left[n]) < len(right[n]) {
			return -1, nil
		}
		if len(left[n]) > len(right[n]) {
			return 1, nil
		}
		if result := strings.Compare(left[n], right[n]); result != 0 {
			return result, nil
		}
	}
	return 0, nil
}

// prepareEntries creates the stable entry without replacing an existing file.
// Human activation separately verifies and migrates legacy entry programs.
func prepareEntries(root, directory string) error {
	name := Binary("telegram-mcp")
	destination := filepath.Join(root, name)
	if _, err := os.Lstat(destination); err == nil {
		return privatefs.CheckExecutable(destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := privatefs.ReadExecutable(filepath.Join(directory, name), maxFile)
	if err != nil {
		return err
	}
	return privatefs.WriteExecutable(destination, data)
}

// CheckEntry verifies the stable program's own capability. A legacy program
// forwarding the probe to a selected child cannot satisfy physical identity.
func CheckEntry(ctx context.Context, executable string) error {
	if err := privatefs.CheckExecutable(executable); err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	return checkEntry(ctx, executable, probeEntry)
}

type entryProbe func(context.Context, string) ([]byte, error)

func checkEntry(ctx context.Context, executable string, probe entryProbe) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := os.Lstat(executable)
	if err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	data, err := probe(ctx, executable)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var info EntryInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	canonical, err := json.Marshal(info)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, data) != nil || !bytes.Equal(compact.Bytes(), canonical) || info.Protocol != EntryProtocol {
		return ErrEntryInvalid
	}
	if !filepath.IsAbs(info.Executable) || filepath.Clean(info.Executable) != info.Executable {
		return ErrEntryInvalid
	}
	if err := privatefs.CheckExecutable(info.Executable); err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	after, err := os.Lstat(executable)
	if err != nil || !os.SameFile(before, after) {
		return errors.Join(ErrEntryInvalid, err)
	}
	reported, err := os.Lstat(info.Executable)
	if err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	if !os.SameFile(after, reported) {
		return ErrEntryMigrationRequired
	}
	return nil
}

func probeEntry(ctx context.Context, executable string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "--entry-info")
	command.WaitDelay = time.Second
	var output entryOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if output.overflow {
			return nil, ErrEntryInvalid
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return nil, errors.Join(ErrEntryMigrationRequired, err)
		}
		return nil, errors.Join(ErrEntryInvalid, err)
	}
	return output.data, nil
}

type entryOutput struct {
	data     []byte
	overflow bool
}

func (b *entryOutput) Write(data []byte) (int, error) {
	if len(b.data)+len(data) > 16*1024 {
		b.overflow = true
		return 0, ErrEntryInvalid
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

// EnsureEntry migrates a managed stable entry using the verified release that
// the human selected. The caller performs release verification before activation.
func EnsureEntry(ctx context.Context, directory string) error {
	root, err := DefaultRoot()
	if err != nil {
		return err
	}
	if ManagedVersion(root, directory) == "" {
		return nil
	}
	return ensureEntry(ctx, root, directory, probeEntry)
}

func ensureEntry(ctx context.Context, root, directory string, probe entryProbe) error {
	stable := filepath.Join(root, Binary("telegram-mcp"))
	if err := privatefs.CheckExecutable(stable); err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	before, err := os.Lstat(stable)
	if err != nil {
		return err
	}
	err = checkEntry(ctx, stable, probe)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrEntryMigrationRequired) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	source := filepath.Join(directory, Binary("telegram-mcp"))
	if err := privatefs.CheckExecutable(source); err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	if err := checkEntry(ctx, source, probe); err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	data, err := privatefs.ReadExecutable(source, maxFile)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := privatefs.ReplaceExecutable(stable, data, before); err != nil {
		return errors.Join(ErrEntryMigrationRequired, err)
	}
	if err := privatefs.CheckExecutable(stable); err != nil {
		return errors.Join(ErrEntryInvalid, err)
	}
	return checkEntry(ctx, stable, probe)
}

// VerifiedRelay additionally checks that a managed entry can follow its selected release.
func VerifiedRelay(ctx context.Context, directory, manual string) (string, error) {
	relay, err := Relay(directory, manual)
	if err != nil {
		return "", err
	}
	root, err := DefaultRoot()
	if err != nil {
		return "", err
	}
	if ManagedVersion(root, directory) != "" {
		if err := CheckEntry(ctx, relay); err != nil {
			return "", err
		}
	}
	return relay, nil
}

// Relay returns the stable managed path, or the explicit manual installation path.
func Relay(directory, manual string) (string, error) {
	root, err := DefaultRoot()
	if err != nil {
		return "", err
	}
	if ManagedVersion(root, directory) == "" {
		return manual, nil
	}
	relay := filepath.Join(root, Binary("telegram-mcp"))
	if err := privatefs.CheckExecutable(relay); err != nil {
		return "", err
	}
	return relay, nil
}
