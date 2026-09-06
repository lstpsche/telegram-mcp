package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

// Serve accepts a bounded number of same-user connections. Each handler owns
// one connection until it returns; cancellation closes all active connections.
func (s *Socket) Serve(ctx context.Context, handle func(context.Context, net.Conn)) error {
	if s == nil || s.listener == nil || handle == nil {
		return errors.New("runtime socket is not initialized")
	}
	slots := make(chan struct{}, 8)
	var handlers sync.WaitGroup
	serveContext, cancel := context.WithCancel(ctx)
	defer handlers.Wait()
	defer cancel()
	stop := context.AfterFunc(serveContext, func() { _ = s.Close() })
	defer stop()
	for {
		if err := serveContext.Err(); err != nil {
			return err
		}
		connection, err := s.listener.Accept()
		if err != nil {
			if serveContext.Err() != nil {
				return serveContext.Err()
			}
			if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
				continue
			}
			return fmt.Errorf("accept runtime socket connection: %w", err)
		}
		if err := VerifyPeer(connection); err != nil {
			_ = connection.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer func() { <-slots }()
				stop := context.AfterFunc(serveContext, func() { _ = connection.Close() })
				defer stop()
				defer connection.Close()
				handle(serveContext, connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}
