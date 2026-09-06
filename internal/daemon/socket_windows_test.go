package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivatePipeRoundTripAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "gateway.sock")
	socket, err := BindSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if second, err := BindSocket(path); err == nil {
		second.Close()
		t.Fatal("accepted second pipe listener")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- socket.Serve(ctx, func(_ context.Context, conn net.Conn) { io.Copy(conn, conn) }) }()
	conn, err := DialSocket(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4)
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != "ping" {
		t.Fatalf("round trip: %q, %v", data, err)
	}
	conn.Close()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener failed to stop")
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if state, err := ProbeSocket(path); err != nil || state != SocketAbsent {
		t.Fatalf("closed pipe: %s, %v", state, err)
	}
}
