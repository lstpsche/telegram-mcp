//go:build !windows

package cli

import (
	"errors"
	"os"
)

func openConsole() (*os.File, *os.File, error) {
	input, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	output, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return nil, nil, errors.Join(err, input.Close())
	}
	return input, output, nil
}
