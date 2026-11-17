//go:build darwin || linux

package main

import (
	"context"
	"io"

	"golang.org/x/sys/unix"
)

func executeEntry(ctx context.Context, executable string, environment []string, _ io.ReadCloser, _ io.WriteCloser, _ io.Writer) (int, error) {
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	// Exec retains the actual inherited descriptors before pollableStdio can
	// change their shared file-status flags.
	return 1, unix.Exec(executable, []string{executable}, environment)
}
