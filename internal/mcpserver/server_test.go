package mcpserver

import (
	"bufio"
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
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serveTestServer(t *testing.T) (string, context.Context) {
	return serveTextTestServer(t, nil)
}

func serveTextTestServer(t *testing.T, service *reader.Service) (string, context.Context) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tmcp-wire-")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(dir, "server.sock")
	socket, err := daemon.BindSocket(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	server := New(func() daemon.Snapshot { return daemon.Snapshot{State: daemon.StateReauthRequired} }, service)
	done := make(chan error, 1)
	go func() {
		done <- socket.Serve(ctx, func(ctx context.Context, connection *net.UnixConn) { Serve(ctx, server, connection) })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("socket handlers did not stop")
		}
		_ = socket.Close()
		_ = os.RemoveAll(dir)
	})
	return socketPath, ctx
}

func TestConcurrentRelayClientsAndSanitizedStatus(t *testing.T) {
	path, ctx := serveTestServer(t)
	sessions := make([]*mcp.ClientSession, 0, 2)
	relayDone := make([]chan error, 0, 2)
	for range 2 {
		inputReader, inputWriter := io.Pipe()
		outputReader, outputWriter := io.Pipe()
		done := make(chan error, 1)
		go func() { done <- daemon.Relay(ctx, path, inputReader, outputWriter) }()
		client := mcp.NewClient(&mcp.Implementation{Name: "wire-test", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outputReader, Writer: inputWriter}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		sessions = append(sessions, session)
		relayDone = append(relayDone, done)
	}
	for _, session := range sessions {
		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(tools.Tools) != 4 {
			t.Fatalf("unexpected tool inventory: %#v", tools.Tools)
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatalf("status error: %v", err)
		}
		content := result.Content[0].(*mcp.TextContent).Text
		var structured map[string]any
		if err := json.Unmarshal([]byte(content), &structured); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(content, `"account_state":"reauth_required"`) || !strings.Contains(content, `"message_reads":false`) || !strings.Contains(content, `"telegram":"unavailable"`) {
			t.Fatalf("false readiness: %s", content)
		}
		bytes, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var mirror map[string]any
		if err := json.Unmarshal(bytes, &mirror); err != nil {
			t.Fatal(err)
		}
		if mirror["request_id"] != structured["request_id"] {
			t.Fatal("text and structured results differ")
		}
		invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{"private-input-marker": "hidden"}})
		if err != nil || !invalid.IsError {
			t.Fatalf("unknown arguments were accepted: %v", err)
		}
		bad := invalid.Content[0].(*mcp.TextContent).Text
		if strings.Contains(bad, "private-input-marker") || strings.Contains(bad, "hidden") || !strings.Contains(bad, "invalid_input") {
			t.Fatalf("unsafe input error: %s", bad)
		}
	}
	_ = sessions[0].Close()
	select {
	case <-relayDone[0]:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop after client close")
	}
	if _, err := sessions[1].CallTool(ctx, &mcp.CallToolParams{Name: "status"}); err != nil {
		t.Fatalf("one client disconnect broke the other: %v", err)
	}
	_ = sessions[1].Close()
	select {
	case <-relayDone[1]:
	case <-time.After(2 * time.Second):
		t.Fatal("second relay did not stop")
	}
}

func TestIndependentJSONRPCClientAndOversizedInput(t *testing.T) {
	path, ctx := serveTestServer(t)
	connection, err := daemon.DialSocket(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(connection)
	exchange := func(request string) map[string]any {
		t.Helper()
		if _, err := io.WriteString(connection, request+"\n"); err != nil {
			t.Fatal(err)
		}
		response, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		if err := json.Unmarshal(response, &message); err != nil {
			t.Fatal(err)
		}
		if message["error"] != nil {
			t.Fatalf("JSON-RPC error: %s", response)
		}
		return message
	}
	response := exchange(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"independent-client","version":"1"}}}`)
	result := response["result"].(map[string]any)
	if result["serverInfo"].(map[string]any)["name"] != "Telegram MCP" {
		t.Fatal("wrong product name")
	}
	if _, err := io.WriteString(connection, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	exchange(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	exchange(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
	// The server can close as soon as the limit is reached, before the write
	// completes. Both an early broken pipe and EOF after a full write are valid.
	_, _ = io.WriteString(connection, strings.Repeat("x", maximumInputFrameBytes+1)+"\n")
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("oversized unterminated frame was accepted")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("server did not close the oversized frame connection")
	}
}

func TestRelayCancellationClosesIdleStreams(t *testing.T) {
	path, parent := serveTestServer(t)
	ctx, cancel := context.WithCancel(parent)
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer inputWriter.Close()
	defer outputReader.Close()
	done := make(chan error, 1)
	go func() { done <- daemon.Relay(ctx, path, inputReader, outputWriter) }()
	if _, err := io.WriteString(inputWriter, "{}\n"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay blocked on idle stdio after cancellation")
	}
}

func TestRelayReportsRemoteDisconnect(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "tmcp-close-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "daemon.sock")
	socket, err := daemon.BindSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	disconnect := make(chan struct{})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- socket.Serve(ctx, func(context.Context, *net.UnixConn) { close(started); <-disconnect })
	}()
	inputReader, inputWriter := io.Pipe()
	defer inputWriter.Close()
	outputReader, outputWriter := io.Pipe()
	defer outputReader.Close()
	done := make(chan error, 1)
	go func() { done <- daemon.Relay(ctx, path, inputReader, outputWriter) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not connect")
	}
	close(disconnect)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("remote disconnect was reported as clean client shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not notice remote disconnect")
	}
	cancel()
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}
