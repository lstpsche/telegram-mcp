//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const terminalPollMilliseconds = 100

func readPasswordBounded(ctx context.Context, file *os.File, maximum int) (result []byte, resultError error) {
	fileDescriptor := int(file.Fd())
	state, err := unix.IoctlGetTermios(fileDescriptor, terminalGetAttributes)
	if err != nil {
		return nil, err
	}
	hidden := *state
	hidden.Lflag &^= unix.ECHO | unix.ECHONL
	hidden.Lflag |= unix.ICANON | unix.ISIG
	hidden.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fileDescriptor, terminalSetAttributes, &hidden); err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fileDescriptor, true); err != nil {
		return nil, errors.Join(err, unix.IoctlSetTermios(fileDescriptor, terminalSetAttributes, state))
	}
	defer func() {
		cleanupError := errors.Join(
			unix.SetNonblock(fileDescriptor, false),
			unix.IoctlSetTermios(fileDescriptor, terminalSetAttributes, state),
		)
		if cleanupError != nil {
			clear(result)
			result = nil
			resultError = errors.Join(resultError, cleanupError)
		}
	}()

	value := make([]byte, 0, min(maximum, 64))
	tooLong := false
	buffer := make([]byte, 256)
	defer clear(buffer)
	for {
		if err := ctx.Err(); err != nil {
			clear(value)
			return nil, err
		}
		pollDescriptors := []unix.PollFd{{Fd: int32(fileDescriptor), Events: unix.POLLIN}}
		ready, err := unix.Poll(pollDescriptors, terminalPollMilliseconds)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			clear(value)
			return nil, err
		}
		if ready == 0 {
			continue
		}
		count, err := unix.Read(fileDescriptor, buffer)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			clear(value)
			return nil, err
		}
		if count == 0 {
			if len(value) == 0 && !tooLong {
				return nil, io.EOF
			}
			return finishPassword(value, tooLong)
		}
		for _, character := range buffer[:count] {
			switch character {
			case '\n':
				return finishPassword(value, tooLong)
			case '\r':
				continue
			default:
				if len(value) < maximum {
					value = append(value, character)
				} else {
					tooLong = true
				}
			}
		}
	}
}

func finishPassword(value []byte, tooLong bool) ([]byte, error) {
	if tooLong {
		clear(value)
		return nil, errors.New("interactive input exceeds the allowed length")
	}
	return value, nil
}
