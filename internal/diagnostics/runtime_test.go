package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/mcpserver"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func testPaths(t *testing.T) daemon.Paths {
	t.Helper()
	tempDir := "/tmp"
	if runtime.GOOS == "windows" {
		tempDir = os.TempDir()
	}
	root, err := os.MkdirTemp(tempDir, "doctor-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if runtime.GOOS == "windows" {
		root = filepath.Join(root, "private")
		if err := privatefs.EnsureDirectory(root); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := daemon.NewPaths(filepath.Join(root, "state"), filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func serve(t *testing.T, paths daemon.Paths, handler func(context.Context, net.Conn)) {
	t.Helper()
	socket, err := daemon.BindSocket(paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- socket.Serve(ctx, handler) }()
	t.Cleanup(func() {
		cancel()
		if err := socket.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("synthetic server did not close")
		}
	})
}

func TestInspectLocalMCPWithoutOpeningMetadata(t *testing.T) {
	paths := testPaths(t)
	if err := privatefs.EnsureDirectory(paths.StateDir); err != nil {
		t.Fatal(err)
	}
	written := []byte("deliberately not a SQLite database; private fixture")
	for _, path := range []string{paths.Database, paths.Lock, filepath.Join(paths.StateDir, "policy.lock"), filepath.Join(paths.StateDir, "secrets.json"), filepath.Join(paths.StateDir, "secrets.lock")} {
		if err := privatefs.WriteFile(path, written, false); err != nil {
			t.Fatal(err)
		}
	}
	server := mcpserver.New(func() daemon.Snapshot { return daemon.Snapshot{State: daemon.StateReauthRequired} }, nil)
	serve(t, paths, func(ctx context.Context, conn net.Conn) { mcpserver.Serve(ctx, server, conn) })
	before, err := os.ReadDir(paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if !report.MCP || report.Socket != daemon.SocketLive || report.AccountState == nil || *report.AccountState != daemon.StateReauthRequired || report.MessageReads == nil || *report.MessageReads || report.Secrets != "not_checked" || report.Metadata != "filesystem_only" {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, check := range report.Files {
		if check.State != "present" {
			t.Fatalf("unexpected file observation: %+v", check)
		}
	}
	after, err := os.ReadDir(paths.StateDir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("metadata entries changed: %v", err)
	}
	for _, entry := range after {
		data, err := os.ReadFile(filepath.Join(paths.StateDir, entry.Name()))
		if err != nil || !bytes.Equal(data, written) {
			t.Fatalf("metadata changed: %v", err)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil || bytes.Contains(encoded, []byte("private fixture")) || bytes.Contains(encoded, []byte("req_")) || bytes.Contains(encoded, []byte(paths.StateDir)) {
		t.Fatalf("unexpected report disclosure: %v", err)
	}
}

func TestInspectMissingStateDoesNotCreateFiles(t *testing.T) {
	paths := testPaths(t)
	report, err := Inspect(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if report.Socket != daemon.SocketAbsent || report.MCP || report.AccountState != nil || report.MessageReads != nil {
		t.Fatalf("missing runtime fabricated status: %+v", report)
	}
	for _, check := range report.Files {
		if check.State != "absent" {
			t.Fatalf("wrong missing observation: %+v", check)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(paths.StateDir))
	if err != nil || len(entries) != 0 {
		t.Fatalf("inspection created state: %v", err)
	}
}

func TestInspectStaleSocketLeavesNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes leave no stale filesystem node")
	}
	paths := testPaths(t)
	if err := privatefs.EnsureDirectory(paths.RuntimeDir); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: paths.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(paths.Socket, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(context.Background(), paths)
	if err != nil || report.Socket != daemon.SocketStale || report.MCP {
		t.Fatalf("wrong stale observation: %+v %v", report, err)
	}
	after, err := os.Lstat(paths.Socket)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("stale socket replaced or removed")
	}
}

func TestInspectRejectsUnsafeFilesWithoutPartialReport(t *testing.T) {
	for _, fixture := range []string{"directory_mode", "file_mode", "symlink", "directory_as_database", "wrong_paths"} {
		t.Run(fixture, func(t *testing.T) {
			if runtime.GOOS == "windows" && (fixture == "directory_mode" || fixture == "file_mode" || fixture == "symlink" || fixture == "writable") {
				t.Skip("Unix permissions and symlink fixture; Windows ACL rejection is tested in privatefs")
			}
			paths := testPaths(t)
			if err := privatefs.EnsureDirectory(paths.StateDir); err != nil {
				t.Fatal(err)
			}
			var err error
			switch fixture {
			case "directory_mode":
				err = os.Chmod(paths.StateDir, 0o755)
			case "file_mode":
				err = os.WriteFile(paths.Database, nil, 0o644)
			case "symlink":
				err = os.Symlink("missing", paths.Database)
			case "directory_as_database":
				err = os.Mkdir(paths.Database, 0o600)
			case "wrong_paths":
				paths.Database = filepath.Join(paths.StateDir, "different")
			}
			if err != nil {
				t.Fatal(err)
			}
			report, err := Inspect(context.Background(), paths)
			if err == nil || !reflect.DeepEqual(report, Report{}) || strings.Contains(err.Error(), paths.StateDir) {
				t.Fatalf("unsafe paths returned report or disclosed path: %+v %v", report, err)
			}
		})
	}
}

func validEnvelope(t *testing.T) map[string]any {
	t.Helper()
	freshness, err := model.NewFreshness(model.FreshnessUnavailable, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := model.NewEnvelope("req_synthetic", freshness, []accountStatus{{AccountState: daemon.StateReady}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func statusResponse(t *testing.T, value map[string]any, textValue map[string]any) []byte {
	t.Helper()
	textBytes, err := json.Marshal(textValue)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"structuredContent": value, "content": []any{map[string]any{"type": "text", "text": string(textBytes)}}}})
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func TestInspectRejectsHostileOrIncompleteStatus(t *testing.T) {
	for _, name := range []string{"malformed", "remote_error", "oversized", "wrong_id", "mirror", "unknown_state", "production", "missing_flag", "null_flag", "null_partial", "partial", "receipt", "cursor", "scope", "warnings", "freshness", "unknown_field", "extra_item", "missing_untrusted"} {
		t.Run(name, func(t *testing.T) {
			paths := testPaths(t)
			value := validEnvelope(t)
			item := value["items"].([]any)[0].(map[string]any)
			switch name {
			case "unknown_state":
				item["account_state"] = "private peer title"
			case "production":
				item["production_login"] = "invalid"
			case "missing_flag":
				delete(item, "message_reads")
			case "null_flag":
				item["message_reads"] = nil
			case "null_partial":
				value["partial"] = nil
			case "partial":
				value["partial"] = true
			case "receipt":
				value["read_effect"] = map[string]any{"kind": "none", "through_message_id": nil}
			case "cursor":
				value["next_cursor"] = "private_cursor"
			case "scope":
				value["scope"] = nil
			case "warnings":
				value["warnings"] = []string{"partial_result"}
			case "freshness":
				value["freshness"].(map[string]any)["telegram"] = "live"
			case "unknown_field":
				value["private_payload"] = "secret"
			case "extra_item":
				value["items"] = []any{item, item}
			case "missing_untrusted":
				delete(value, "untrusted_content")
			}
			textValue := value
			if name == "mirror" {
				textValue = validEnvelope(t)
				textValue["request_id"] = "req_different"
			}
			response := statusResponse(t, value, textValue)
			switch name {
			case "malformed":
				response = []byte("private invalid JSON\n")
			case "remote_error":
				response = []byte(`{"jsonrpc":"2.0","id":2,"error":{"message":"private remote error"}}` + "\n")
			case "oversized":
				response = []byte(strings.Repeat("p", maximumFrameBytes+1) + "\n")
			case "wrong_id":
				response = bytes.Replace(response, []byte(`"id":2`), []byte(`"id":3`), 1)
			}
			serve(t, paths, scriptedStatus(response))
			report, err := Inspect(context.Background(), paths)
			if err == nil || !reflect.DeepEqual(report, Report{}) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid response escaped safe error boundary: %+v %v", report, err)
			}
		})
	}
}

func scriptedStatus(response []byte) func(context.Context, net.Conn) {
	return func(ctx context.Context, conn net.Conn) {
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"private server name","version":"private version"},"instructions":"private instructions"}}`+"\n"); err != nil {
			return
		}
		for range 2 {
			if _, err := reader.ReadBytes('\n'); err != nil {
				return
			}
		}
		_, _ = conn.Write(response)
	}
}

func TestInspectTimeoutAndCancellation(t *testing.T) {
	paths := testPaths(t)
	serve(t, paths, func(ctx context.Context, conn net.Conn) {
		_, _ = io.Copy(io.Discard, conn)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	report, err := Inspect(ctx, paths)
	if err == nil || !reflect.DeepEqual(report, Report{}) || time.Since(started) > time.Second {
		t.Fatalf("silent peer was not bounded: %+v %v", report, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, paths); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}

func TestInspectRejectsUnsafeAncestryBeforeReportingMissingState(t *testing.T) {
	for _, fixture := range []string{"symlink", "writable"} {
		t.Run(fixture, func(t *testing.T) {
			if runtime.GOOS == "windows" && (fixture == "directory_mode" || fixture == "file_mode" || fixture == "symlink" || fixture == "writable") {
				t.Skip("Unix permissions and symlink fixture; Windows ACL rejection is tested in privatefs")
			}
			paths := testPaths(t)
			root := filepath.Dir(paths.StateDir)
			if fixture == "writable" {
				if err := os.Chmod(root, 0o777); err != nil {
					t.Fatal(err)
				}
			} else {
				link := filepath.Join(root, "alias")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				var err error
				paths, err = daemon.NewPaths(filepath.Join(link, "state"), filepath.Join(link, "runtime"))
				if err != nil {
					t.Fatal(err)
				}
			}
			report, err := Inspect(context.Background(), paths)
			if err == nil || !reflect.DeepEqual(report, Report{}) {
				t.Fatalf("unsafe ancestry accepted: %+v %v", report, err)
			}
		})
	}
}

func TestInspectOnlyInitializesAndCallsStatus(t *testing.T) {
	paths := testPaths(t)
	value := validEnvelope(t)
	response := statusResponse(t, value, value)
	requests := make(chan []map[string]json.RawMessage, 1)
	serve(t, paths, func(ctx context.Context, conn net.Conn) {
		reader := bufio.NewReader(conn)
		var observed []map[string]json.RawMessage
		for index := range 3 {
			frame, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var request map[string]json.RawMessage
			if err := json.Unmarshal(frame, &request); err != nil {
				return
			}
			observed = append(observed, request)
			if index == 0 {
				if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"synthetic","version":"1"}}}`+"\n"); err != nil {
					return
				}
			}
		}
		if _, err := conn.Write(response); err != nil {
			return
		}
		if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
			return
		}
		requests <- observed
	})
	report, err := Inspect(context.Background(), paths)
	if err != nil || !report.MCP {
		t.Fatalf("synthetic MCP failed: %+v %v", report, err)
	}
	select {
	case observed := <-requests:
		for index, method := range []string{`"initialize"`, `"notifications/initialized"`, `"tools/call"`} {
			if string(observed[index]["method"]) != method {
				t.Fatal("unexpected diagnostic MCP method")
			}
		}
		if string(observed[2]["params"]) != `{"name":"status","arguments":{}}` {
			t.Fatal("diagnostics requested something besides status")
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not close after status")
	}
}

func TestStrictStatusFieldsRejectDuplicateKeys(t *testing.T) {
	for _, data := range []string{`{"key":1,"key":2}`, `{"key":1,"other":2}`, `{"key":null,"key":false}`} {
		if exactKeys([]byte(data), "key") {
			t.Fatal("duplicate or additional field accepted")
		}
	}
}

func TestInspectRejectsInvalidInitialization(t *testing.T) {
	for _, result := range []string{
		`{"protocolVersion":"invalid","capabilities":{},"serverInfo":{"name":"synthetic","version":"1"}}`,
		`{"protocolVersion":"2025-11-25","capabilities":null,"serverInfo":{"name":"synthetic","version":"1"}}`,
		`{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{}}`,
	} {
		paths := testPaths(t)
		serve(t, paths, func(ctx context.Context, conn net.Conn) {
			if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
				return
			}
			_, _ = io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"result":`+result+"}\n")
		})
		if report, err := Inspect(context.Background(), paths); err == nil || !reflect.DeepEqual(report, Report{}) {
			t.Fatalf("invalid initialization accepted: %+v %v", report, err)
		}
	}
}
