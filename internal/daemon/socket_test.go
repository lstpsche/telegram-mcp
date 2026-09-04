package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSocketLifecycleAndProbe(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(shortTempDir(t), "runtime", socketFilename)
	socket, err := BindSocket(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- socket.ServeProbes(ctx) }()

	state, err := ProbeSocket(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if state != SocketLive {
		t.Fatalf("ProbeSocket() = %q, want %q", state, SocketLive)
	}
	if second, err := BindSocket(socketPath); second != nil || !errors.Is(err, ErrSocketInUse) {
		t.Fatalf("second BindSocket() = %v, %v", second, err)
	}

	cancel()
	select {
	case err := <-serveDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeProbes() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeProbes() did not stop after cancellation")
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket path remains after close: %v", err)
	}
}

func TestBindSocketReplacesOnlyVerifiedStaleSocket(t *testing.T) {
	t.Parallel()

	runtimeDir := filepath.Join(shortTempDir(t), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(runtimeDir, socketFilename)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := ProbeSocket(socketPath)
	if err != nil || state != SocketStale {
		t.Fatalf("ProbeSocket(stale) = %q, %v", state, err)
	}
	replacement, err := BindSocket(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSocketProbeFailureDoesNotRemoveActiveSocket(t *testing.T) {
	t.Parallel()

	runtimeDir := filepath.Join(shortTempDir(t), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(runtimeDir, socketFilename)
	active, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	active.SetUnlinkOnClose(false)
	t.Cleanup(func() {
		_ = active.Close()
		_ = os.Remove(socketPath)
	})
	if err := os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	originalIdentity, err := inspectSocket(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	failingDialer := func(string, string, time.Duration) (net.Conn, error) {
		return nil, unix.EMFILE
	}
	if state, err := probeSocket(socketPath, failingDialer); state != "" || !errors.Is(err, ErrSocketInUse) {
		t.Fatalf("probeSocket() = %q, %v; want fail-closed ErrSocketInUse", state, err)
	}
	if socket, err := bindSocket(socketPath, failingDialer); socket != nil || !errors.Is(err, ErrSocketInUse) {
		t.Fatalf("bindSocket() = %v, %v; want ErrSocketInUse", socket, err)
	}
	currentIdentity, err := inspectSocket(socketPath)
	if err != nil {
		t.Fatalf("active socket was removed after an inconclusive probe: %v", err)
	}
	if currentIdentity != originalIdentity {
		t.Fatal("active socket identity changed after an inconclusive probe")
	}
}

func TestBindSocketRejectsMaliciousPaths(t *testing.T) {
	t.Parallel()

	tests := map[string]func(t *testing.T, path string){
		"regular file": func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, path string) {
			target := path + ".target"
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		},
		"wrong permissions": func(t *testing.T, path string) {
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			listener.SetUnlinkOnClose(false)
			if err := os.Chmod(path, 0o666); err != nil {
				t.Fatal(err)
			}
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, prepare := range tests {
		name, prepare := name, prepare
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runtimeDir := filepath.Join(shortTempDir(t), "runtime")
			if err := os.Mkdir(runtimeDir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(runtimeDir, socketFilename)
			prepare(t, path)
			if socket, err := BindSocket(path); err == nil {
				_ = socket.Close()
				t.Fatal("BindSocket() accepted an unsafe path")
			}
		})
	}
}

func TestBindSocketRejectsLongPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(shortTempDir(t), strings.Repeat("x", 120))
	if socket, err := BindSocket(path); err == nil {
		_ = socket.Close()
		t.Fatal("BindSocket() accepted an overlong path")
	}
}

func TestProbeSocketRejectsUnsafeRuntimeDirectory(t *testing.T) {
	t.Parallel()

	runtimeDir := filepath.Join(shortTempDir(t), "runtime")
	if err := os.Mkdir(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if state, err := ProbeSocket(filepath.Join(runtimeDir, socketFilename)); err == nil {
		t.Fatalf("ProbeSocket() = %q, nil; want fail-closed error", state)
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "tgc-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove temporary directory: %v", err)
		}
	})
	return directory
}
