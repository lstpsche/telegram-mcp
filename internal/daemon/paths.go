package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

const (
	applicationDirectory = "Telegram MCP"
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

// DefaultPaths resolves the current OS user configuration and cache locations.
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
	if filepath.Dir(stateDir) == stateDir || filepath.Dir(runtimeDir) == runtimeDir {
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

func ensurePrivateDirectory(path string) error  { return privatefs.EnsureDirectory(path) }
func inspectPrivateDirectory(path string) error { return privatefs.CheckDirectory(path) }
