package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/mcpserver"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func TestAutomaticStartupPreservesMCPHandshakeAndLifetime(t *testing.T) {
	base, err := os.MkdirTemp("", "tmcp-boot-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(base, "runtime")
	if err := privatefs.EnsureDirectory(runtime); err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.NewPaths(filepath.Join(base, "state"), runtime)
	if err != nil {
		t.Fatal(err)
	}
	lifetime, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	input, writeInput := io.Pipe()
	readOutput, output := io.Pipe()
	defer writeInput.Close()
	defer readOutput.Close()
	initialize := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"synthetic-startup\",\"version\":\"1\"}}}\n"
	// A client may write before the OS has finished starting the daemon.
	written := make(chan error, 1)
	go func() { _, err := io.WriteString(writeInput, initialize); written <- err }()
	var socket *daemon.Socket
	session := &fakeStartup{start: func(context.Context) error {
		var err error
		socket, err = daemon.BindSocket(paths.Socket)
		if err != nil {
			return err
		}
		server := mcpserver.New(func() daemon.Snapshot { return daemon.Snapshot{State: daemon.StateReauthRequired} }, nil, nil)
		go func() {
			_ = socket.Serve(lifetime, func(ctx context.Context, conn net.Conn) { mcpserver.Serve(ctx, server, conn) })
		}()
		return nil
	}}
	startup, finishStartup := context.WithTimeout(lifetime, time.Second)
	connection, err := connectWithStartup(startup, paths.Socket, daemon.DialSocket,
		func(ctx context.Context, action func(startupSession) error) error { return action(session) })
	finishStartup()
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	bridged := make(chan error, 1)
	go func() { bridged <- relayStdio(lifetime, connection, input, output) }()
	decoder := json.NewDecoder(readOutput)
	var response struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := decoder.Decode(&response); err != nil || response.ID != 1 {
		t.Fatalf("initialize: id=%d error=%v", response.ID, err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	requests := "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"status\",\"arguments\":{}}}\n"
	if _, err := io.WriteString(writeInput, requests); err != nil {
		t.Fatal(err)
	}
	seen := make(map[int]bool, 2)
	var status json.RawMessage
	for range 2 {
		if err := decoder.Decode(&response); err != nil || (response.ID != 2 && response.ID != 3) || seen[response.ID] {
			t.Fatalf("MCP response: id=%d error=%v", response.ID, err)
		}
		seen[response.ID] = true
		if response.ID == 3 {
			status = append(status, response.Result...)
		}
	}
	if !strings.Contains(string(status), "reauth_required") || session.calls != 1 {
		t.Fatalf("status=%s starts=%d", status, session.calls)
	}
	stop()
	select {
	case <-bridged:
	case <-time.After(time.Second):
		t.Fatal("established relay did not cancel")
	}
}
