package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func entryFixture(t *testing.T) (string, string, string) {
	t.Helper()
	i, _ := releaseFixture(t)
	directory := filepath.Join(i.Root, "versions", "1.5.0")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(i.Root, Binary("telegram-mcp"))
	source := filepath.Join(directory, Binary("telegram-mcp"))
	if err := privatefs.WriteExecutable(stable, []byte("legacy entry")); err != nil {
		t.Fatal(err)
	}
	if err := privatefs.WriteExecutable(source, []byte("new entry")); err != nil {
		t.Fatal(err)
	}
	return i.Root, directory, stable
}

func entryJSON(path string) []byte {
	data, _ := json.Marshal(EntryInfo{Protocol: EntryProtocol, Executable: path})
	return data
}

func TestEntryProbeRequiresPhysicalIdentityAndStrictProtocol(t *testing.T) {
	_, directory, stable := entryFixture(t)
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"own identity", entryJSON(stable), nil},
		{"forwarded child", entryJSON(filepath.Join(directory, Binary("telegram-mcp"))), ErrEntryMigrationRequired},
		{"empty identity", []byte("{\"entry_protocol\":1,\"executable\":\"\"}"), ErrEntryInvalid},
		{"unknown protocol", []byte("{\"entry_protocol\":2,\"executable\":\"/unknown\"}"), ErrEntryInvalid},
		{"unknown field", append(entryJSON(stable)[:len(entryJSON(stable))-1], []byte(",\"extra\":true}")...), ErrEntryInvalid},
		{"duplicate field", []byte("{\"entry_protocol\":1,\"entry_protocol\":1,\"executable\":\"/unknown\"}"), ErrEntryInvalid},
		{"trailing data", append(entryJSON(stable), []byte("{}")...), ErrEntryInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkEntry(context.Background(), stable, func(context.Context, string) ([]byte, error) { return tc.data, nil })
			if (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("probe error %v, expected %v", err, tc.want)
			}
		})
	}
	t.Run("changed executable", func(t *testing.T) {
		err := checkEntry(context.Background(), stable, func(_ context.Context, path string) ([]byte, error) {
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			if err := privatefs.ReplaceExecutable(path, []byte("other"), info); err != nil {
				return nil, err
			}
			return entryJSON(path), nil
		})
		if !errors.Is(err, ErrEntryInvalid) {
			t.Fatal("accepted changed executable", err)
		}
	})
}

func TestManagedEntryMigrationVerifiesSourceAndRetainsCompatibleEntry(t *testing.T) {
	root, directory, stable := entryFixture(t)
	probe := func(_ context.Context, path string) ([]byte, error) {
		data, err := privatefs.ReadExecutable(path, 64)
		if err != nil {
			return nil, err
		}
		if string(data) == "legacy entry" {
			return nil, ErrEntryMigrationRequired
		}
		return entryJSON(path), nil
	}
	if err := ensureEntry(context.Background(), root, directory, probe); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(stable)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureEntry(context.Background(), root, directory, probe); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(stable)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("replaced compatible stable entry", err)
	}
	data, err := privatefs.ReadExecutable(stable, 64)
	if err != nil || string(data) != "new entry" {
		t.Fatal("migration bytes mismatch", err)
	}
}

func TestManagedEntryMigrationDoesNotReplaceOnInvalidSourceOrCancellation(t *testing.T) {
	for _, mode := range []string{"old source", "malformed response", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			root, directory, stable := entryFixture(t)
			before, err := os.Lstat(stable)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probe := func(_ context.Context, path string) ([]byte, error) {
				if mode == "cancelled" {
					cancel()
				}
				if mode == "malformed response" {
					return []byte("{}"), nil
				}
				return nil, ErrEntryMigrationRequired
			}
			if err := ensureEntry(ctx, root, directory, probe); err == nil {
				t.Fatal("accepted invalid migration")
			}
			after, err := os.Lstat(stable)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("failed migration replaced entry", err)
			}
		})
	}
}

func TestEntryOutputIsBounded(t *testing.T) {
	var output entryOutput
	if _, err := output.Write(make([]byte, 16*1024+1)); err == nil || !output.overflow || len(output.data) != 0 {
		t.Fatal("entry output bound was not enforced")
	}
}

func TestEntryProbeAcceptsWindowsPathCaseForTheSamePhysicalFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path casing is case insensitive")
	}
	_, _, stable := entryFixture(t)
	alias := filepath.Join(filepath.Dir(stable), strings.ToUpper(filepath.Base(stable)))
	err := checkEntry(context.Background(), stable, func(context.Context, string) ([]byte, error) {
		return entryJSON(alias), nil
	})
	if err != nil {
		t.Fatal("rejected the same physical Windows entry", err)
	}
}

func TestStableEntriesSurviveVersionChange(t *testing.T) {
	i, archive := releaseFixture(t)
	old, err := i.installArchive(context.Background(), "0.2.0", archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareEntries(i.Root, old); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(i.Root, Binary("telegram-mcp"))
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(i.Root, "versions", "0.3.0")
	if err := privatefs.EnsureDirectory(next); err != nil {
		t.Fatal(err)
	}
	if err := privatefs.WriteExecutable(filepath.Join(next, Binary("telegram-mcp")), []byte("new executable")); err != nil {
		t.Fatal(err)
	}
	if err := prepareEntries(i.Root, next); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("stable relay replaced")
	}
	data, err := privatefs.ReadExecutable(path, 64)
	if err != nil || string(data) != "synthetic executable" {
		t.Fatal("relay bytes changed", err)
	}
	if ManagedVersion(i.Root, next) != "0.3.0" || ManagedVersion(i.Root, filepath.Join(i.Root, "unmanaged", "0.3.0")) != "" {
		t.Fatal("incorrect layout recognition")
	}
}
