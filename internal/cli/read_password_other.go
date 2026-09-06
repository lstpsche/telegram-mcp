//go:build !darwin && !linux

package cli

import (
	"context"
	"errors"
	"os"

	"golang.org/x/term"
)

func readPasswordBounded(ctx context.Context, file *os.File, maximum int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := term.ReadPassword(int(file.Fd()))
	if err != nil {
		clear(value)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		clear(value)
		return nil, err
	}
	if len(value) > maximum {
		clear(value)
		return nil, errors.New("interactive input exceeds the allowed length")
	}
	return value, nil
}
