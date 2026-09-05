package mcpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/logging"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const peerPattern = `^tgpeer:v1:(self|user|chat|channel):[1-9][0-9]*$`
const messagePattern = `^tgmsg:v1:(self|user|chat|channel):[1-9][0-9]*:[1-9][0-9]*$`

func registerTextTools(server *mcp.Server, service *reader.Service) {
	open := true
	for _, tool := range []*mcp.Tool{
		{Name: "list_chats", Description: "List up to 20 currently granted conversations. Fetches only granted peer metadata and does not acknowledge history.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":1,"maximum":100}}}`), OutputSchema: textOutputSchema("chats"), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &open}},
		{Name: "list_messages", Description: "Read bounded authorized text history, newest first. Before is an exclusive message reference, never authority. Marks the separately authorized dialog prefix read before releasing bodies. next_cursor is null; use a returned ID as before for an older window.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["peer"],"properties":{"peer":{"type":"string","pattern":"` + peerPattern + `"},"before":{"type":"string","pattern":"` + messagePattern + `"},"limit":{"type":"integer","minimum":1,"maximum":100}}}`), OutputSchema: textOutputSchema("messages"), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: &open}},
		{Name: "get_message_context", Description: "Read one authorized target and bounded older/newer neighbors. Zero neighbors reads just the target. Missing or denied targets yield no bodies. Marks the authorized dialog prefix read before releasing text.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["message"],"properties":{"message":{"type":"string","pattern":"` + messagePattern + `"},"before":{"type":"integer","minimum":0,"maximum":49},"after":{"type":"integer","minimum":0,"maximum":49}}}`), OutputSchema: textOutputSchema("messages"), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: &open}},
		{Name: "search_messages", Description: "Search authorized text in one peer and return snippets up to 240 characters, newest first, without marking history read. Follow an ID with get_message_context for the acknowledged full body. Query is trimmed, then limited to 256 characters (1024 input bytes). Repeat the same peer, query and limit with next_cursor. Live edits/deletions can change results; a cursor is not authority or a snapshot.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["peer","query"],"properties":{"peer":{"type":"string","pattern":"` + peerPattern + `"},"query":{"type":"string","minLength":1,"maxLength":1024},"limit":{"type":"integer","minimum":1,"maximum":100},"cursor":{"type":"string","minLength":1,"maxLength":4096}}}`), OutputSchema: textOutputSchema("search"), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &open}},
		{Name: "list_unread", Description: "Return unread counts and manual unread flags for all currently granted dialogs (at most 20). Counts cover the whole dialog, including messages outside the body grant's author/range. Returns no bodies or dates and does not mark history read.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`), OutputSchema: textOutputSchema("unread"), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &open}},
	} {
		var schema jsonschema.Schema
		if err := json.Unmarshal(tool.InputSchema.(json.RawMessage), &schema); err != nil {
			panic("invalid static text input schema")
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			panic("unresolvable static text input schema")
		}
		server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := "req_" + rand.Text()
			arguments := request.Params.Arguments
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			var result reader.Result
			err := validateArguments(arguments, resolved)
			if err == nil {
				result, err = callText(ctx, service, id, request.Params.Name, arguments)
			}
			if err != nil {
				category := model.TextErrorCategory(err)
				logging.New(os.Stderr, nil).Warn(ctx, logging.EventOperationFailed, logging.ComponentField(logging.ComponentDaemon), logging.RequestIDField(id), logging.ErrorCategoryField(category))
				envelope, buildErr := model.NewErrorEnvelope(category, id, nil)
				if buildErr != nil {
					return nil, errors.New("text operation failed")
				}
				encoded, buildErr := json.Marshal(envelope)
				if buildErr != nil {
					return nil, errors.New("text operation failed")
				}
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
			}
			return &mcp.CallToolResult{StructuredContent: result.JSON, Content: []mcp.Content{&mcp.TextContent{Text: string(result.JSON)}}}, nil
		})
	}
}

func validateArguments(arguments []byte, schema *jsonschema.Resolved) error {
	invalid := func() error { return model.TextError(model.ErrorInvalidInput, nil) }
	// Decode keys separately because encoding/json otherwise accepts duplicates.
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return invalid()
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalid()
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return invalid()
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return invalid()
		}
	}
	if _, err := decoder.Token(); err != nil {
		return invalid()
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return invalid()
	}
	var input any
	if err := json.Unmarshal(arguments, &input); err != nil {
		return invalid()
	}
	if err := schema.Validate(input); err != nil {
		return invalid()
	}
	return nil
}

func callText(ctx context.Context, service *reader.Service, id, name string, args json.RawMessage) (reader.Result, error) {
	invalid := func() (reader.Result, error) { return reader.Result{}, model.TextError(model.ErrorInvalidInput, nil) }
	var query model.HistoryQuery
	limit := model.DefaultPageSize
	switch name {
	case "list_unread":
		if _, err := model.DecodeStrict[struct{}](args); err != nil {
			return invalid()
		}
		if service == nil {
			return reader.Result{}, model.TextError(model.ErrorNotReady, nil)
		}
		return service.ListUnread(ctx, id)
	case "search_messages":
		input, err := model.DecodeStrict[struct {
			Peer   string  `json:"peer"`
			Query  string  `json:"query"`
			Limit  *int    `json:"limit"`
			Cursor *string `json:"cursor"`
		}](args)
		if err != nil {
			return invalid()
		}
		peer, err := model.ParsePeerID(input.Peer)
		if err != nil {
			return invalid()
		}
		if input.Limit != nil {
			limit = *input.Limit
		}
		query, err := model.NormalizeSearchQuery(input.Query)
		if err != nil || model.ValidatePageSize(limit) != nil {
			return invalid()
		}
		token := ""
		if input.Cursor != nil {
			token = *input.Cursor
			if token == "" || len(token) > 4096 {
				return invalid()
			}
		}
		if service == nil {
			return reader.Result{}, model.TextError(model.ErrorNotReady, nil)
		}
		return service.Search(ctx, id, peer, query, limit, token)
	case "list_chats":
		input, err := model.DecodeStrict[struct {
			Limit *int `json:"limit"`
		}](args)
		if err != nil {
			return invalid()
		}
		if input.Limit != nil {
			limit = *input.Limit
		}
	case "list_messages":
		input, err := model.DecodeStrict[struct {
			Peer   string  `json:"peer"`
			Before *string `json:"before"`
			Limit  *int    `json:"limit"`
		}](args)
		if err != nil {
			return invalid()
		}
		query.Peer, err = model.ParsePeerID(input.Peer)
		if err != nil {
			return invalid()
		}
		if input.Before != nil {
			before, err := model.ParseMessageID(*input.Before)
			if err != nil || before.Peer() != query.Peer {
				return invalid()
			}
			query.Before = before.TelegramID()
		}
		if input.Limit != nil {
			limit = *input.Limit
		}
	case "get_message_context":
		input, err := model.DecodeStrict[struct {
			Message string `json:"message"`
			Before  int    `json:"before"`
			After   int    `json:"after"`
		}](args)
		if err != nil {
			return invalid()
		}
		target, err := model.ParseMessageID(input.Message)
		if err != nil || input.Before < 0 || input.After < 0 || input.Before > 49 || input.After > 49 {
			return invalid()
		}
		query.Peer = target.Peer()
		query.Target = target.TelegramID()
		query.BeforeCount = input.Before
		query.AfterCount = input.After
		limit = input.Before + input.After + 1
	default:
		return invalid()
	}
	if err := model.ValidatePageSize(limit); err != nil {
		return invalid()
	}
	if service == nil {
		return reader.Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	if name == "list_chats" {
		return service.ListChats(ctx, id, limit)
	}
	query.Limit = limit
	return service.Messages(ctx, id, query)
}

func textOutputSchema(kind string) json.RawMessage {
	item := `{"type":"object","additionalProperties":false,"required":["id","title"],"properties":{"id":{"type":"string","pattern":"` + peerPattern + `"},"title":{"type":"string"}}}`
	if kind == "messages" {
		item = `{"type":"object","additionalProperties":false,"required":["id","author","date","text"],"properties":{"id":{"type":"string","pattern":"` + messagePattern + `"},"author":{"type":"string","pattern":"` + peerPattern + `"},"date":{"type":"string"},"text":{"type":"string"}}}`
	}
	next := `{"type":"null"}`
	if kind == "search" {
		item = `{"type":"object","additionalProperties":false,"required":["id","author","date","snippet","snippet_truncated"],"properties":{"id":{"type":"string","pattern":"` + messagePattern + `"},"author":{"type":"string","pattern":"` + peerPattern + `"},"date":{"type":"string"},"snippet":{"type":"string","maxLength":240},"snippet_truncated":{"type":"boolean"}}}`
		next = `{"type":["string","null"],"maxLength":4096}`
	}
	if kind == "unread" {
		item = `{"type":"object","additionalProperties":false,"required":["peer","unread_count","unread_mark"],"properties":{"peer":{"type":"string","pattern":"` + peerPattern + `"},"unread_count":{"type":"integer","minimum":0,"maximum":2147483647},"unread_mark":{"type":"boolean"}}}`
	}
	return json.RawMessage(strings.NewReplacer("ITEM_SCHEMA", item, "CURSOR_SCHEMA", next).Replace(`{"type":"object","additionalProperties":false,"required":["schema_version","request_id","freshness","partial","read_effect","items","next_cursor","warnings","untrusted_content"],"properties":{"schema_version":{"const":"1"},"request_id":{"type":"string"},"freshness":{"type":"object","additionalProperties":false,"required":["telegram","checked_at"],"properties":{"telegram":{"enum":["live","recovering","stale","partial","unavailable"]},"checked_at":{"type":"string"}}},"partial":{"type":"boolean"},"read_effect":{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"enum":["none","history_marked_read"]},"through_message_id":{"type":"string"}}},"items":{"type":"array","maxItems":100,"items":ITEM_SCHEMA},"next_cursor":CURSOR_SCHEMA,"warnings":{"type":"array","items":{"enum":["partial_result","freshness_degraded"]}},"untrusted_content":{"const":true}}}`))
}
