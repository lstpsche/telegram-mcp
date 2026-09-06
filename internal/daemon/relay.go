package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Relay forwards bytes only. It owns the passed streams for this invocation
// and closes them on disconnect or cancellation to unblock both directions.
func Relay(ctx context.Context, path string, input io.ReadCloser, output io.WriteCloser) error {
	connection, err := DialSocket(ctx, path)
	if err != nil {
		return fmt.Errorf("daemon unavailable: %w", err)
	}
	defer connection.Close()
	writer, ok := connection.(interface{ CloseWrite() error })
	if !ok {
		return errors.New("daemon connection does not support half-close")
	}
	defer input.Close()
	defer output.Close()
	stop := context.AfterFunc(ctx, func() {
		_ = connection.Close()
		_ = input.Close()
		_ = output.Close()
	})
	defer stop()
	incoming := make(chan error, 1)
	outgoing := make(chan error, 1)
	go func() {
		_, err := io.Copy(connection, input)
		incoming <- err
		_ = writer.CloseWrite()
	}()
	go func() {
		_, err := io.Copy(output, connection)
		outgoing <- err
	}()
	select {
	case err = <-incoming:
		if err != nil {
			_ = connection.Close()
			_ = output.Close()
		}
		outputError := <-outgoing
		if err == nil {
			err = outputError
		}
	case err = <-outgoing:
		select {
		case inputError := <-incoming:
			if err == nil {
				err = inputError
			}
		default:
			if err == nil {
				err = errors.New("daemon disconnected")
			}
			_ = connection.Close()
			_ = input.Close()
			<-incoming
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("relay connection failed: %w", err)
	}
	return nil
}
