package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"golang.org/x/sys/windows"
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

func TestPipeDialClassifiesOnlyDefiniteAbsence(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "gateway.sock")
	for _, test := range []struct {
		name        string
		cause       error
		unavailable bool
	}{
		{"missing pipe", windows.ERROR_FILE_NOT_FOUND, true},
		{"missing pipe path", windows.ERROR_PATH_NOT_FOUND, true},
		{"busy pipe", windows.ERROR_PIPE_BUSY, false},
		{"access denied", windows.ERROR_ACCESS_DENIED, false},
		{"timeout", context.DeadlineExceeded, false},
		{"joined unsafe", errors.Join(ErrUnsafeSocket, windows.ERROR_FILE_NOT_FOUND), false},
		{"joined cancellation", errors.Join(context.Canceled, windows.ERROR_FILE_NOT_FOUND), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := dialPipeSocket(context.Background(), path, func(context.Context, string) (net.Conn, error) {
				return nil, test.cause
			})
			if err == nil || errors.Is(err, ErrDaemonUnavailable) != test.unavailable {
				t.Fatalf("classification: %v, unavailable=%v, want %v", err, errors.Is(err, ErrDaemonUnavailable), test.unavailable)
			}
		})
	}
	missing := filepath.Join(directory, "missing", "gateway.sock")
	if _, err := DialSocket(context.Background(), missing); !errors.Is(err, ErrDaemonUnavailable) {
		t.Fatalf("safe absent directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dial created state: %v", err)
	}
	file := filepath.Join(directory, "ordinary")
	if err := privatefs.WriteFile(file, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := DialSocket(context.Background(), filepath.Join(file, "gateway.sock")); err == nil || errors.Is(err, ErrDaemonUnavailable) {
		t.Fatalf("unsafe directory: %v", err)
	}
}
