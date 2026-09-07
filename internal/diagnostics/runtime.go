// Package diagnostics inspects local installation health without opening account
// storage or fetching Telegram data.
package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"syscall"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

const (
	maximumFrameBytes = 32 * 1024
	maximumReplyBytes = 128 * 1024
	protocolVersion   = "2025-11-25"
)

type Report struct {
	Files        []FileCheck        `json:"files"`
	Socket       daemon.SocketState `json:"socket"`
	MCP          bool               `json:"mcp"`
	AccountState *daemon.State      `json:"account_state"`
	MessageReads *bool              `json:"message_reads"`
	Secrets      string             `json:"secrets"`
	Metadata     string             `json:"metadata"`
}

// inspectionError preserves the cause for classification without allowing a
// peer-controlled JSON value or filesystem path into diagnostic output.
type inspectionError struct{ cause error }

func (e *inspectionError) Error() string { return "local diagnostics failed" }
func (e *inspectionError) Unwrap() error { return e.cause }

// Inspect performs only filesystem metadata reads and an MCP initialize/status
// exchange. Missing or stale sockets are observations, never runtime readiness.
func Inspect(ctx context.Context, paths daemon.Paths) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	report, err := inspect(ctx, paths)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		return Report{}, &inspectionError{err}
	}
	return report, nil
}

func inspect(ctx context.Context, paths daemon.Paths) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	files, err := inspectFiles(paths)
	if err != nil {
		return Report{}, err
	}
	report := Report{Files: files, Secrets: "not_checked", Metadata: "filesystem_only"}
	connection, err := daemon.DialSocket(ctx, paths.Socket)
	if err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		switch {
		case errors.Is(err, os.ErrNotExist):
			report.Socket = daemon.SocketAbsent
		case errors.Is(err, syscall.ECONNREFUSED):
			report.Socket = daemon.SocketStale
		default:
			return Report{}, err
		}
		return report, nil
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return Report{}, err
	}
	reader := bufio.NewReaderSize(io.LimitReader(connection, maximumReplyBytes), maximumFrameBytes)
	if _, err := io.WriteString(connection, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"telegram-mcp-doctor","version":"1"}}}`+"\n"); err != nil {
		return Report{}, err
	}
	initialized, err := readResponse(reader, 1)
	if err != nil {
		return Report{}, err
	}
	var handshake struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(initialized, &handshake); err != nil {
		return Report{}, err
	}
	if handshake.ProtocolVersion != protocolVersion || !jsonObject(handshake.Capabilities) || handshake.ServerInfo.Name == "" || handshake.ServerInfo.Version == "" {
		return Report{}, errors.New("invalid MCP initialization response")
	}
	if _, err := io.WriteString(connection, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`+"\n"); err != nil {
		return Report{}, err
	}
	result, err := readResponse(reader, 2)
	if err != nil {
		return Report{}, err
	}
	status, err := decodeStatus(result)
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	report.Socket, report.MCP = daemon.SocketLive, true
	report.AccountState, report.MessageReads = &status.AccountState, &status.MessageReads
	return report, nil
}

func readResponse(reader *bufio.Reader, id int) (json.RawMessage, error) {
	frame, err := reader.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	if !exactKeys(frame, "jsonrpc", "id", "result") {
		return nil, errors.New("invalid MCP response fields")
	}
	response, err := model.DecodeStrict[struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
	}](frame)
	if err != nil {
		return nil, err
	}
	if response.JSONRPC != "2.0" || response.ID != id || !jsonObject(response.Result) {
		return nil, errors.New("invalid MCP response")
	}
	return response.Result, nil
}

type accountStatus struct {
	AccountState    daemon.State `json:"account_state"`
	MessageReads    bool         `json:"message_reads"`
	ProductionLogin bool         `json:"production_login"`
}

func decodeStatus(data []byte) (accountStatus, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return accountStatus{}, err
	}
	keys := []string{"structuredContent", "content"}
	if flag, present := fields["isError"]; present {
		if string(flag) != "false" {
			return accountStatus{}, errors.New("MCP status did not succeed")
		}
		keys = append(keys, "isError")
	}
	if !exactKeys(data, keys...) {
		return accountStatus{}, errors.New("invalid MCP status result fields")
	}
	result, err := model.DecodeStrict[struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}](data)
	if err != nil {
		return accountStatus{}, err
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
		return accountStatus{}, errors.New("invalid MCP status result")
	}
	var content []json.RawMessage
	if err := json.Unmarshal(fields["content"], &content); err != nil {
		return accountStatus{}, err
	}
	if !exactKeys(content[0], "type", "text") {
		return accountStatus{}, errors.New("invalid MCP status text fields")
	}
	structured, err := decodeEnvelope(result.StructuredContent)
	if err != nil {
		return accountStatus{}, err
	}
	text, err := decodeEnvelope([]byte(result.Content[0].Text))
	if err != nil {
		return accountStatus{}, err
	}
	if !reflect.DeepEqual(structured, text) {
		return accountStatus{}, errors.New("MCP status representations differ")
	}
	return structured.Items[0], nil
}

func decodeEnvelope(data []byte) (model.Envelope[accountStatus], error) {
	envelope, err := model.DecodeStrict[model.Envelope[accountStatus]](data)
	if err != nil {
		return envelope, err
	}
	if err := envelope.Validate(); err != nil {
		return envelope, err
	}
	if !exactKeys(data, "schema_version", "request_id", "freshness", "partial", "read_effect", "items", "next_cursor", "warnings", "untrusted_content") || envelope.Scope != nil || envelope.Partial || envelope.NextCursor != nil || len(envelope.Warnings) != 0 || envelope.ReadEffect.Kind != model.ReadEffectNone || envelope.Freshness.Telegram != model.FreshnessUnavailable || len(envelope.Items) != 1 {
		return envelope, errors.New("invalid MCP status metadata")
	}
	var raw struct {
		Items      []json.RawMessage `json:"items"`
		Partial    json.RawMessage   `json:"partial"`
		Freshness  json.RawMessage   `json:"freshness"`
		ReadEffect json.RawMessage   `json:"read_effect"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return envelope, err
	}
	if string(raw.Partial) != "false" || !exactKeys(raw.Freshness, "telegram", "checked_at") || !exactKeys(raw.ReadEffect, "kind") {
		return envelope, errors.New("invalid MCP status metadata fields")
	}
	if !exactKeys(raw.Items[0], "account_state", "message_reads", "production_login") {
		return envelope, errors.New("invalid MCP account status")
	}
	switch envelope.Items[0].AccountState {
	case daemon.StateStarting, daemon.StateLocked, daemon.StateConnecting, daemon.StateReady, daemon.StateReauthRequired, daemon.StateStopping, daemon.StateStopped, daemon.StateFailed:
	default:
		return envelope, errors.New("invalid MCP account state")
	}
	// Null must not be silently decoded into a false readiness or login flag.
	var flags map[string]json.RawMessage
	if err := json.Unmarshal(raw.Items[0], &flags); err != nil {
		return envelope, err
	}
	for _, key := range []string{"message_reads", "production_login"} {
		if string(flags[key]) != "true" && string(flags[key]) != "false" {
			return envelope, errors.New("invalid MCP account flag")
		}
	}
	return envelope, nil
}

func jsonObject(data []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(data, &object) == nil && object != nil
}

func exactKeys(data []byte, keys ...string) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	object := make(map[string]bool, len(keys))
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return false
		}
		key, ok := name.(string)
		if !ok || object[key] {
			return false
		}
		object[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	if _, err := decoder.Token(); err != nil || len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if !object[key] {
			return false
		}
	}
	return true
}
