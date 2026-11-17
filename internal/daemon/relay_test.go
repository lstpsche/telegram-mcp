package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type halfCloseConnection struct {
	net.Conn
	writeClosed chan struct{}
	once        sync.Once
}

func (c *halfCloseConnection) CloseWrite() error {
	c.once.Do(func() { close(c.writeClosed) })
	return nil
}

type relayOutput struct {
	bytes.Buffer
	closed bool
}

func (o *relayOutput) Close() error { o.closed = true; return nil }

func TestRelayConnectionPreservesBytesAndHalfClose(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	connection := &halfCloseConnection{Conn: client, writeClosed: make(chan struct{})}
	request := "{\"jsonrpc\":\"2.0\",\"id\":7}\n\x00\xff"
	response := "{\"result\":\"ready\"}\n\x00\xfe"
	serverDone := make(chan error, 1)
	go func() {
		body := make([]byte, len(request))
		if _, err := io.ReadFull(server, body); err != nil {
			serverDone <- err
			return
		}
		if string(body) != request {
			serverDone <- errors.New("request bytes changed")
			return
		}
		<-connection.writeClosed
		_, err := io.WriteString(server, response)
		serverDone <- errors.Join(err, server.Close())
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var output relayOutput
	if err := RelayConnection(ctx, connection, io.NopCloser(strings.NewReader(request)), &output); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if output.String() != response || !output.closed {
		t.Fatalf("output bytes or ownership changed: %q, closed=%v", output.String(), output.closed)
	}
	if _, err := client.Write([]byte("closed")); err == nil {
		t.Fatal("relay did not close its connection")
	}
}

func TestRelayConnectionCancellationUnblocksBothDirections(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	connection := &halfCloseConnection{Conn: client, writeClosed: make(chan struct{})}
	input, inputWriter := io.Pipe()
	defer inputWriter.Close()
	outputReader, output := io.Pipe()
	defer outputReader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RelayConnection(ctx, connection, input, output) }()
	writeDone := make(chan error, 1)
	go func() { _, err := server.Write([]byte("blocked output")); writeDone <- err }()
	// A successful server write means the relay is now blocked on output while
	// its input remains blocked independently.
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("relay did not read the response")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation left relay workers blocked")
	}
}

func TestRelayConnectionRejectsUnsupportedConnectionAndClosesResources(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	input, inputWriter := io.Pipe()
	defer inputWriter.Close()
	var output relayOutput
	if err := RelayConnection(context.Background(), client, input, &output); err == nil {
		t.Fatal("accepted connection without half-close")
	}
	if !output.closed {
		t.Fatal("output was not closed")
	}
	if _, err := inputWriter.Write([]byte("closed")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("input was not closed: %v", err)
	}
}
