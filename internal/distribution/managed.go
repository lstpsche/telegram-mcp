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

// prepareEntries publishes the stable unified entry once. Human arguments
// dispatch through the service record; no-argument invocation relays MCP bytes.
// The executable is never overwritten while another process may be using it.
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
