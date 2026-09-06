package distribution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

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

// prepareEntries publishes stable regular entry programs once. The relay is
// byte-only; the human control program dispatches through the service record.
// Neither entry is overwritten while another process may be executing it.
func prepareEntries(root, directory string) error {
	for _, name := range []string{Binary("telegram-mcp"), Binary("telegram-mcpctl")} {
		destination := filepath.Join(root, name)
		if _, err := os.Lstat(destination); err == nil {
			if err := privatefs.CheckExecutable(destination); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := privatefs.ReadExecutable(filepath.Join(directory, name), maxFile)
		if err != nil {
			return err
		}
		if err := privatefs.WriteExecutable(destination, data); err != nil {
			return err
		}
	}
	return nil
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
