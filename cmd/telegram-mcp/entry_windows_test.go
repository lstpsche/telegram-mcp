package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"golang.org/x/sys/windows"
)

func TestSelectedWindowsChildExitsWhenWrapperIsKilled(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "private")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	// Compile the actual process-supervision implementation into a small wrapper.
	implementation, err := os.ReadFile("entry_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	implementationPath := filepath.Join(base, "entry_windows.go")
	if err := os.WriteFile(implementationPath, implementation, 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "main.go")
	program := "package main\nimport(\"context\";\"fmt\";\"os\";\"time\")\nfunc main(){if os.Getenv(\"TELEGRAM_MCP_ENTRY_JOB_CHILD\")==\"1\"{fmt.Println(os.Getpid());for{time.Sleep(time.Second)}};env:=append(os.Environ(),\"TELEGRAM_MCP_ENTRY_JOB_CHILD=1\");code,err:=executeEntry(context.Background(),os.Args[0],env,os.Stdin,os.Stdout,os.Stderr);if err!=nil{fmt.Fprintln(os.Stderr,\"wrapper failed\");os.Exit(1)};os.Exit(code)}\n"
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "wrapper.exe")
	build, stopBuild := context.WithTimeout(context.Background(), time.Minute)
	defer stopBuild()
	if output, err := exec.CommandContext(build, "go", "build", "-o", executable, source, implementationPath).CombinedOutput(); err != nil {
		t.Fatalf("build supervised wrapper: %v: %s", err, output)
	}
	lifetime, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(lifetime, executable)
	// Keep the inherited input open so pipe EOF cannot explain child termination.
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer inputWriter.Close()
	command.Stdin = input
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	type childResult struct {
		line string
		err  error
	}
	started := make(chan childResult, 1)
	go func() {
		line, err := bufio.NewReader(output).ReadString('\n')
		started <- childResult{line, err}
	}()
	var pid uint64
	select {
	case result := <-started:
		if result.err != nil {
			_ = command.Wait()
			t.Fatalf("child startup: %v; %s", result.err, diagnostics.String())
		}
		pid, err = strconv.ParseUint(strings.TrimSpace(result.line), 10, 32)
		if err != nil || pid == 0 {
			t.Fatalf("invalid synthetic child process: %q", result.line)
		}
	case <-lifetime.Done():
		t.Fatal("wrapper did not create a child", lifetime.Err())
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	defer windows.TerminateProcess(child, 1)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	state, err := windows.WaitForSingleObject(child, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("hard wrapper termination left a child: state=%d error=%v", state, err)
	}
}

func TestSelectedWindowsChildUsesLifetimeContext(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "private")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "main.go")
	program := "package main\nimport(\"fmt\";\"os\";\"time\")\nfunc main(){fmt.Println(\"connected\");if os.Getenv(\"TELEGRAM_MCP_ENTRY_TEST_HOLD\")==\"1\"{for{time.Sleep(time.Second)}};time.Sleep(150*time.Millisecond);fmt.Println(\"alive\")}\n"
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "child.exe")
	build, stopBuild := context.WithTimeout(context.Background(), time.Minute)
	defer stopBuild()
	if output, err := exec.CommandContext(build, "go", "build", "-o", executable, source).CombinedOutput(); err != nil {
		t.Fatalf("build synthetic child: %v: %s", err, output)
	}
	for _, scenario := range []string{"startup expires", "parent cancels"} {
		t.Run(scenario, func(t *testing.T) {
			lifetime, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stdin, inputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			defer inputWriter.Close()
			outputReader, stdout, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer outputReader.Close()
			defer stdout.Close()
			stderr, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			environment := entryEnvironment(os.Environ(), time.Now().Add(50*time.Millisecond))
			if scenario == "parent cancels" {
				environment = append(environment, "TELEGRAM_MCP_ENTRY_TEST_HOLD=1")
			}
			type result struct {
				code int
				err  error
			}
			done := make(chan result, 1)
			go func() {
				code, err := executeEntry(lifetime, executable, environment, stdin, stdout, stderr)
				done <- result{code, err}
			}()
			reader := bufio.NewReader(outputReader)
			readLine := func() string {
				t.Helper()
				type lineResult struct {
					line string
					err  error
				}
				lines := make(chan lineResult, 1)
				go func() {
					line, err := reader.ReadString('\n')
					lines <- lineResult{line, err}
				}()
				select {
				case result := <-lines:
					if result.err != nil {
						t.Fatal("child output", result.err)
					}
					return result.line
				case <-lifetime.Done():
					t.Fatal("child output deadline", lifetime.Err())
					return ""
				}
			}
			if line := readLine(); line != "connected\n" {
				t.Fatalf("child did not connect: %q", line)
			}
			if scenario == "parent cancels" {
				cancel()
				select {
				case result := <-done:
					if !errors.Is(result.err, context.Canceled) {
						t.Fatal("child cancellation cause lost", result.err)
					}
				case <-time.After(time.Second):
					t.Fatal("parent cancellation left a child running")
				}
				return
			}
			if line := readLine(); line != "alive\n" {
				t.Fatalf("startup timeout ended a connected child: %q", line)
			}
			select {
			case result := <-done:
				if result.err != nil || result.code != 0 {
					t.Fatal("child lifetime", result.code, result.err)
				}
			case <-time.After(time.Second):
				t.Fatal("child was not reaped")
			}
		})
	}
}
