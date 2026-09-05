package main

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"golang.org/x/sys/unix"
)

// relayStdio makes inherited files interruptible before starting either copy.
// In-memory streams already supply their own Close behavior.
func relayStdio(ctx context.Context, path string, input io.ReadCloser, output io.WriteCloser) (resultError error) {
	if file, ok := input.(*os.File); ok {
		prepared, restore, err := pollableStdio(file)
		if err != nil {
			return err
		}
		defer func() { resultError = errors.Join(resultError, restore()) }()
		input = prepared
	}
	if file, ok := output.(*os.File); ok {
		prepared, restore, err := pollableStdio(file)
		if err != nil {
			return err
		}
		defer func() { resultError = errors.Join(resultError, restore()) }()
		output = prepared
	}
	return daemon.Relay(ctx, path, input, output)
}

func pollableStdio(file *os.File) (*os.File, func() error, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	// Regular files do not wait for a pipe peer and are not pollable on macOS.
	if info.Mode().IsRegular() {
		return file, func() error { return nil }, nil
	}
	original := file.Fd()
	flags, err := unix.FcntlInt(original, unix.F_GETFL, 0)
	if err != nil {
		return nil, nil, err
	}
	descriptor, err := unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.SetNonblock(descriptor, true); err != nil {
		return nil, nil, errors.Join(err, unix.Close(descriptor))
	}
	// NewFile registers an already-nonblocking descriptor with Go's poller;
	// changing flags on os.Stdin/os.Stdout alone does not register them.
	prepared := os.NewFile(uintptr(descriptor), file.Name())
	restore := func() error {
		// Relay normally closes the duplicate. Close also covers setup failure.
		closeError := prepared.Close()
		if errors.Is(closeError, os.ErrClosed) {
			closeError = nil
		}
		// Dup shares status flags with inherited descriptors, including any copy
		// retained by the parent. Restore blocking mode after workers have stopped.
		restoreError := unix.SetNonblock(int(original), flags&unix.O_NONBLOCK != 0)
		runtime.KeepAlive(file)
		return errors.Join(closeError, restoreError)
	}
	if err := prepared.SetDeadline(time.Time{}); err != nil {
		return nil, nil, errors.Join(err, restore())
	}
	return prepared, restore, nil
}
