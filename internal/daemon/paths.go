package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	applicationDirectory = "TgContext"
	databaseFilename     = "metadata.db"
	lockFilename         = "account.lock"
	socketFilename       = "gateway.sock"
)

// Paths contains the fixed local paths for the single-account runtime.
type Paths struct {
	StateDir   string
	RuntimeDir string
	Database   string
	Lock       string
	Socket     string
}

// DefaultPaths resolves macOS Application Support and Caches locations without
// consulting process environment variables for overrides.
func DefaultPaths() (Paths, error) {
	configurationRoot, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user configuration directory: %w", err)
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user cache directory: %w", err)
	}
	return NewPaths(
		filepath.Join(configurationRoot, applicationDirectory),
		filepath.Join(cacheRoot, applicationDirectory),
	)
}

// NewPaths constructs explicit paths for tests and composition roots.
func NewPaths(stateDir, runtimeDir string) (Paths, error) {
	if !filepath.IsAbs(stateDir) || !filepath.IsAbs(runtimeDir) {
		return Paths{}, errors.New("state and runtime directories must be absolute")
	}
	stateDir = filepath.Clean(stateDir)
	runtimeDir = filepath.Clean(runtimeDir)
	if stateDir == string(filepath.Separator) || runtimeDir == string(filepath.Separator) {
		return Paths{}, errors.New("state and runtime directories must not be filesystem roots")
	}
	return Paths{
		StateDir:   stateDir,
		RuntimeDir: runtimeDir,
		Database:   filepath.Join(stateDir, databaseFilename),
		Lock:       filepath.Join(stateDir, lockFilename),
		Socket:     filepath.Join(runtimeDir, socketFilename),
	}, nil
}

func ensurePrivateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return errors.New("private directory path must be a non-root absolute path")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	return inspectPrivateDirectory(path)
}

func inspectPrivateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return errors.New("private directory path must be a non-root absolute path")
	}
	var status unix.Stat_t
	if err := unix.Lstat(path, &status); err != nil {
		return fmt.Errorf("inspect private directory: %w", err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("private directory must be a real directory")
	}
	if status.Uid != uint32(os.Geteuid()) {
		return errors.New("private directory must be owned by the current user")
	}
	if permissions := os.FileMode(status.Mode).Perm(); permissions != 0o700 {
		return fmt.Errorf("private directory permissions are %04o, require 0700", permissions)
	}
	return nil
}
