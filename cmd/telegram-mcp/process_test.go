package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"golang.org/x/sys/unix"
)

func TestRelayProcessHelper(t *testing.T) {
	if os.Getenv("TELEGRAM_MCP_RELAY_HELPER") != "1" {
		return
	}
	os.Args = []string{"telegram-mcp"}
	main()
}

func TestInheritedPipesUnblockOnDisconnectAndCancellation(t *testing.T) {
	for _, scenario := range []string{"disconnect", "idle cancellation", "blocked output cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			home, err := os.MkdirTemp("/tmp", "tmcp-relay-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(home)
			// DefaultPaths uses the platform's per-user cache directory.
			t.Setenv("HOME", home)
			paths, err := daemon.DefaultPaths()
			if err != nil {
				t.Fatal(err)
			}
			socket, err := daemon.BindSocket(paths.Socket)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			connections := make(chan *net.UnixConn, 1)
			release := make(chan struct{})
			defer close(release)
			go func() {
				_ = socket.Serve(ctx, func(_ context.Context, connection *net.UnixConn) {
					connections <- connection
					<-release
				})
			}()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRelayProcessHelper$")
			command.Env = []string{"HOME=" + home, "TELEGRAM_MCP_RELAY_HELPER=1"}
			inputReader, input, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer inputReader.Close()
			command.Stdin = inputReader
			output, outputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			defer outputWriter.Close()
			command.Stdout = outputWriter
			descriptors := []uintptr{inputReader.Fd(), outputWriter.Fd()}
			flags := make([]int, len(descriptors))
			for i, descriptor := range descriptors {
				flags[i], err = unix.FcntlInt(descriptor, unix.F_GETFL, 0)
				if err != nil {
					t.Fatal(err)
				}
			}
			var diagnostics bytes.Buffer
			command.Stderr = &diagnostics
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			var connection *net.UnixConn
			select {
			case connection = <-connections:
			case <-ctx.Done():
				_ = command.Wait()
				t.Fatal("relay did not connect")
			}
			if _, err := io.WriteString(input, "ping"); err != nil {
				t.Fatal(err)
			}
			var request [4]byte
			if _, err := io.ReadFull(connection, request[:]); err != nil || string(request[:]) != "ping" {
				t.Fatalf("relay input: %q, %v", request, err)
			}
			if scenario == "blocked output cancellation" {
				if err := connection.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				_, err := connection.Write(bytes.Repeat([]byte("x"), 4<<20))
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("expected blocked relay output, got %v", err)
				}
			}
			if scenario == "disconnect" {
				_ = connection.Close()
			} else if err := command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("relay exit: %v", err)
				}
			case <-time.After(2 * time.Second):
				cancel()
				<-done
				t.Fatal("relay hung with inherited stdio pipes open")
			}
			for i, descriptor := range descriptors {
				actual, err := unix.FcntlInt(descriptor, unix.F_GETFL, 0)
				if err != nil || actual&unix.O_NONBLOCK != flags[i]&unix.O_NONBLOCK {
					t.Fatalf("inherited blocking mode was not restored: got %d, want %d, %v", actual, flags[i], err)
				}
			}
			category := "cancelled"
			if scenario == "disconnect" {
				category = "not_ready"
			}
			if !strings.Contains(diagnostics.String(), category) || strings.Count(diagnostics.String(), "\n") != 1 || strings.Contains(diagnostics.String(), filepath.Base(home)) {
				t.Fatalf("unexpected diagnostics: %s", diagnostics.String())
			}
		})
	}
}
