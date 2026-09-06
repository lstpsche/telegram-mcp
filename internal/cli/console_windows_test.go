package cli

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestNativeConsoleInput(t *testing.T) {
	if os.Getenv("TELEGRAM_MCP_NATIVE_SERVICE_TEST") != "1" {
		t.Skip("requires disposable Windows console qualification")
	}
	if os.Getenv("TELEGRAM_MCP_CONSOLE_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestNativeConsoleInput$")
		cmd.Env = append(os.Environ(), "TELEGRAM_MCP_CONSOLE_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native console child: %v: %s", err, output)
		}
		return
	}
	terminal, err := OpenTerminal()
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	input := windows.Handle(terminal.input.Fd())
	var original uint32
	if err := windows.GetConsoleMode(input, &original); err != nil {
		t.Fatal(err)
	}
	type result struct {
		value int
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := terminal.ReadAPIID(context.Background())
		done <- result{value, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var mode uint32
		if err := windows.GetConsoleMode(input, &mode); err != nil {
			t.Fatal(err)
		}
		if mode&windows.ENABLE_ECHO_INPUT == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hidden input did not disable console echo")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// INPUT_RECORD contains an aligned KEY_EVENT_RECORD union.
	type inputRecord struct {
		eventType, padding uint16
		keyDown            int32
		repeat, key, scan  uint16
		character          uint16
		control            uint32
	}
	var records []inputRecord
	for _, char := range "12345\r" {
		records = append(records, inputRecord{eventType: 1, keyDown: 1, repeat: 1, character: uint16(char)})
	}
	writeInput := windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")
	var written uint32
	ok, _, err := writeInput.Call(uintptr(input), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&written)))
	if ok == 0 || written != uint32(len(records)) {
		t.Fatalf("inject synthetic console input: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.value != 12345 {
			t.Fatalf("hidden input round trip: value=%d error=%v", got.value, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hidden input did not complete")
	}
	var restored uint32
	if err := windows.GetConsoleMode(input, &restored); err != nil || restored != original {
		t.Fatalf("console mode not restored: %v", err)
	}
}
