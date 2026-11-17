//go:build darwin || linux

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
	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/service"
	"golang.org/x/sys/unix"
)

type entryServiceExit int

func (e entryServiceExit) Error() string { return "synthetic registration absent" }
func (e entryServiceExit) ExitCode() int { return int(e) }

type entryServiceRunner struct {
	unitPath string
	enabled  bool
}

func (r *entryServiceRunner) Run(ctx context.Context, program string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if program == "/bin/launchctl" && len(args) == 2 && args[0] == "print" && strings.Count(args[1], "/") == 2 {
		return entryServiceExit(113)
	}
	if program == "/usr/bin/systemctl" && len(args) >= 2 {
		switch args[1] {
		case "enable":
			r.enabled = true
		case "disable":
			r.enabled = false
		}
	}
	return nil
}

func (r *entryServiceRunner) Query(ctx context.Context, program string, _ ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if program != "/usr/bin/systemctl" {
		return nil, errors.New("unexpected synthetic service query")
	}
	if _, err := os.Lstat(r.unitPath); errors.Is(err, os.ErrNotExist) {
		return []byte("LoadState=not-found\nUnitFileState=\nActiveState=inactive\nFragmentPath=\nDropInPaths=\nNeedDaemonReload=no\n"), nil
	} else if err != nil {
		return nil, err
	}
	mode := "disabled"
	if r.enabled {
		mode = "enabled"
	}
	return []byte("LoadState=loaded\nUnitFileState=" + mode + "\nActiveState=inactive\nFragmentPath=" + r.unitPath + "\nDropInPaths=\nNeedDaemonReload=no\n"), nil
}

func TestManagedEntryExecPreservesBytesCancellationAndInheritedFlags(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("per-user service excludes root")
	}
	base, err := os.MkdirTemp("/tmp", "tmcp-entry-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	// Build the distributed program rather than launching an instrumented test
	// runner, whose runtime startup/exit is unrelated to the release deadlines.
	executable := filepath.Join(base, "relay")
	build, stopBuild := context.WithTimeout(context.Background(), time.Minute)
	defer stopBuild()
	buildCommand := exec.CommandContext(build, "go", "build", "-race=false", "-o", executable, ".")
	buildCommand.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := buildCommand.CombinedOutput(); err != nil {
		t.Fatalf("build synthetic-profile relay: %v: %s", err, output)
	}
	testProgram, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := privatefs.EnsureDirectory(home); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
	} {
		t.Setenv(name, value)
	}
	root, err := distribution.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "versions", "1.5.0")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(root, distribution.Binary("telegram-mcp"))
	for _, path := range []string{stable, filepath.Join(directory, "telegram-mcp"), filepath.Join(directory, "telegram-mcpd")} {
		if err := privatefs.WriteExecutable(path, testProgram); err != nil {
			t.Fatal(err)
		}
	}
	runner := &entryServiceRunner{unitPath: filepath.Join(home, ".config", "systemd", "user", service.Label+".service")}
	manager, err := service.New(home, os.Geteuid(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), directory); err != nil {
		t.Fatal(err)
	}
	// The actual entry handles this before either human dispatch or MCP startup.
	if err := distribution.CheckEntry(context.Background(), stable); err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	socket, err := daemon.BindSocket(paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	lifetime, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- socket.Serve(lifetime, func(_ context.Context, connection net.Conn) {
			defer connection.Close()
			var request [4]byte
			if _, err := io.ReadFull(connection, request[:]); err == nil {
				_, _ = connection.Write(request[:])
			}
			<-release
		})
	}()
	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputReader.Close()
	defer inputWriter.Close()
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outputReader.Close()
	defer outputWriter.Close()
	descriptors := []uintptr{inputReader.Fd(), outputWriter.Fd()}
	before := make([]int, len(descriptors))
	for i, fd := range descriptors {
		before[i], err = unix.FcntlInt(fd, unix.F_GETFL, 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	command := exec.CommandContext(lifetime, stable)
	command.Stdin, command.Stdout = inputReader, outputWriter
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// Do not let test failure leave a child holding inherited handles.
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	if _, err := io.WriteString(inputWriter, "ping"); err != nil {
		t.Fatal(err)
	}
	if err := outputReader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var response [4]byte
	if _, err := io.ReadFull(outputReader, response[:]); err != nil || string(response[:]) != "ping" {
		t.Fatalf("stable entry byte forwarding: %q %v", response, err)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("cancelled relay returned success")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatal(err)
		}
	}
	for i, fd := range descriptors {
		after, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
		if err != nil || before[i]&unix.O_NONBLOCK != after&unix.O_NONBLOCK {
			t.Fatal("selected relay did not restore inherited flags", err)
		}
	}
	if strings.Count(diagnostics.String(), "\n") != 1 || !strings.Contains(diagnostics.String(), "cancelled") {
		t.Fatalf("unexpected stable-entry diagnostics: %s", diagnostics.String())
	}
}
