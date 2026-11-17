package daemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

// ErrDaemonUnavailable marks a positively absent endpoint or a verified refused
// socket. Other dial failures do not authorize an automatic service start.
var ErrDaemonUnavailable = errors.New("the daemon endpoint is unavailable")

func definiteDialFailure(err error, causes ...error) bool {
	var combined interface{ Unwrap() []error }
	var networkError net.Error
	if errors.As(err, &combined) || errors.As(err, &networkError) && networkError.Timeout() {
		return false
	}
	for _, cause := range causes {
		if errors.Is(err, cause) {
			return true
		}
	}
	return false
}

// inspectDialDirectory distinguishes absence from unsafe existing paths without
// creating state. Ancestor validation must succeed before absence is accepted.
func inspectDialDirectory(path string) error {
	if err := privatefs.CheckAncestors(path); err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.Join(ErrDaemonUnavailable, err)
		}
		return err
	}
	if err := privatefs.CheckDirectory(path); err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	return nil
}

func absentEndpoint(path string, cause error) error {
	if err := inspectDialDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return errors.Join(ErrDaemonUnavailable, cause)
}
