package mcpserver

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/buildinfo"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)

const maximumInputFrameBytes = 64 * 1024

// New registers only content-free status. Account mutation and message reads
// cannot be reached through this server.
func New(snapshot func() daemon.Snapshot) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: buildinfo.Product, Version: buildinfo.Version}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Instructions: "Report account status with the status tool. Message reads and account changes are unavailable. Authentication is performed by the human operator outside MCP.",
	})
	schema, err := jsonschema.For[model.Envelope[status]](nil)
	if err != nil {
		panic("invalid static status schema")
	}
	closed := false
	server.AddTool(&mcp.Tool{
		Name: "status", Description: "Inspect local account state. Does not fetch Telegram content or change read receipts. Message access and production login are unavailable.",
		OutputSchema: schema,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		requestID := "req_" + rand.Text()
		arguments := request.Params.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		if _, err := model.DecodeStrict[struct{}](arguments); err != nil {
			result, _ := model.NewErrorEnvelope(model.ErrorInvalidInput, requestID, nil)
			encoded, _ := json.Marshal(result)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, errors.New("request cancelled")
		}
		state := snapshot()
		freshness, err := model.NewFreshness(model.FreshnessUnavailable, time.Now())
		if err != nil {
			return nil, errors.New("status unavailable")
		}
		result, err := model.NewEnvelope(requestID, freshness, []status{{AccountState: state.State, MessageReads: false, ProductionLogin: false}})
		if err != nil {
			return nil, errors.New("status unavailable")
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, errors.New("status unavailable")
		}
		return &mcp.CallToolResult{StructuredContent: result, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
	})
	return server
}

type status struct {
	AccountState    daemon.State `json:"account_state"`
	MessageReads    bool         `json:"message_reads"`
	ProductionLogin bool         `json:"production_login"`
}

// Serve runs an SDK session with bounded newline framing and input rate.
// Session errors stay local and cannot expose request material through logs.
func Serve(ctx context.Context, server *mcp.Server, connection *net.UnixConn) {
	reader := &frameReader{connection: connection, reader: bufio.NewReaderSize(connection, maximumInputFrameBytes), ctx: ctx, limiter: rate.NewLimiter(20, 4)}
	session, err := server.Connect(ctx, &mcp.IOTransport{Reader: reader, Writer: &deadlineWriter{connection}}, nil)
	if err != nil {
		return
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	_ = session.Wait()
}

type frameReader struct {
	connection net.Conn
	reader     *bufio.Reader
	ctx        context.Context
	limiter    *rate.Limiter
	pending    []byte
}

func (r *frameReader) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if err := r.limiter.Wait(r.ctx); err != nil {
			return 0, errors.New("request cancelled")
		}
		if err := r.connection.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
			return 0, errors.New("connection unavailable")
		}
		line, err := r.reader.ReadSlice('\n')
		if err != nil {
			return 0, io.EOF
		}
		r.pending = line
	}
	n := copy(output, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
func (r *frameReader) Close() error { return r.connection.Close() }

// Bound blocked clients independently of the input deadline.
type deadlineWriter struct{ net.Conn }

func (w *deadlineWriter) Write(data []byte) (int, error) {
	if err := w.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, errors.New("connection unavailable")
	}
	return w.Conn.Write(data)
}
