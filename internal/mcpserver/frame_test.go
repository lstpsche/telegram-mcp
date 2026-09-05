package mcpserver

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type frameDeadlineConn struct {
	net.Conn
	deadlines chan time.Time
	shorten   bool
}

func (c *frameDeadlineConn) SetReadDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	if c.shorten && !deadline.IsZero() {
		deadline = time.Now().Add(30 * time.Millisecond)
	}
	return c.Conn.SetReadDeadline(deadline)
}

func TestIdleFrameWaitHasNoDeadlineAndResetsAfterEachFrame(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := &frameDeadlineConn{Conn: server, deadlines: make(chan time.Time, 8)}
	reader := &frameReader{connection: conn, reader: bufio.NewReaderSize(conn, maximumInputFrameBytes), ctx: context.Background(), limiter: rate.NewLimiter(20, 4)}
	for range 2 {
		done := make(chan error, 1)
		go func() {
			frame := make([]byte, 3)
			_, err := io.ReadFull(reader, frame)
			if err == nil && string(frame) != "{}\n" {
				err = errors.New("wrong frame")
			}
			done <- err
		}()
		select {
		case deadline := <-conn.deadlines:
			if !deadline.IsZero() {
				t.Fatal("idle session acquired a read deadline")
			}
		case <-time.After(time.Second):
			t.Fatal("frame read did not begin")
		}
		if _, err := io.WriteString(client, "{"); err != nil {
			t.Fatal(err)
		}
		select {
		case deadline := <-conn.deadlines:
			if deadline.IsZero() || time.Until(deadline) <= 0 || time.Until(deadline) > time.Minute {
				t.Fatal("partial frame lacks bounded completion deadline")
			}
		case <-time.After(time.Second):
			t.Fatal("partial frame did not acquire deadline")
		}
		if _, err := io.WriteString(client, "}\n"); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestIncompleteFrameTimeoutPreservesCauseAndReleasesNoBytes(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := &frameDeadlineConn{Conn: server, deadlines: make(chan time.Time, 8), shorten: true}
	reader := &frameReader{connection: conn, reader: bufio.NewReaderSize(conn, maximumInputFrameBytes), ctx: context.Background(), limiter: rate.NewLimiter(20, 4)}
	done := make(chan error, 1)
	go func() {
		n, err := reader.Read(make([]byte, 1024))
		if n != 0 {
			t.Error("partial frame released bytes")
		}
		done <- err
	}()
	if _, err := io.WriteString(client, "{"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatal("frame timeout cause was lost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial frame did not time out")
	}
}
