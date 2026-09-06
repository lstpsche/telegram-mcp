package diagnostics

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

type FileCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func inspectFiles(paths daemon.Paths) ([]FileCheck, error) {
	expected, err := daemon.NewPaths(paths.StateDir, paths.RuntimeDir)
	if err != nil {
		return nil, err
	}
	if paths != expected {
		return nil, errors.New("diagnostic paths do not match runtime paths")
	}
	for _, directory := range []string{paths.StateDir, paths.RuntimeDir} {
		if err := inspectAncestors(directory); err != nil {
			return nil, err
		}
	}
	checks := []struct {
		name, path string
		directory  bool
	}{
		{"state_directory", paths.StateDir, true},
		{"runtime_directory", paths.RuntimeDir, true},
		{"database", paths.Database, false},
		{"account_lock", paths.Lock, false},
		{"policy_lock", filepath.Join(paths.StateDir, "policy.lock"), false},
		{"secrets", filepath.Join(paths.StateDir, "secrets.json"), false},
		{"secrets_lock", filepath.Join(paths.StateDir, "secrets.lock"), false},
	}
	result := make([]FileCheck, 0, len(checks))
	for _, check := range checks {
		var err error
		if check.directory {
			err = privatefs.CheckDirectory(check.path)
		} else {
			err = privatefs.CheckFile(check.path)
		}
		if errors.Is(err, os.ErrNotExist) {
			result = append(result, FileCheck{check.name, "absent"})
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, FileCheck{check.name, "present"})
	}
	return result, nil
}
