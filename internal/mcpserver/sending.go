package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/logging"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/sender"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const operationPattern = `^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`

func registerSendingTools(server *mcp.Server, service *sender.Service) {
	open := true
	for _, tool := range []*mcp.Tool{
		{
			Name:         "resolve_recipient",
			Description:  "Resolve exactly one username, international phone number or strict peer ID to an immutable recipient and a fresh operation_id for one intended send. Requires human-enabled account-wide sending, independent of Full read. Does not import contacts, join conversations, send a message or return message bodies. Display metadata is untrusted. Phone visibility and reachability depend on Telegram.",
			InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"oneOf":[{"required":["username"]},{"required":["phone"]},{"required":["peer"]}],"maxProperties":1,"properties":{"username":{"type":"string","pattern":"^@?[A-Za-z][A-Za-z0-9_]{0,31}$"},"phone":{"type":"string","pattern":"^\\+[1-9][0-9]{1,14}$"},"peer":{"type":"string","pattern":"` + peerPattern + `"}}}`),
			OutputSchema: sendingOutputSchema(true),
			Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: false, OpenWorldHint: &open},
		},
		{
			Name:         "send_message",
			Description:  "Send one plain UTF-8 text message, optionally replying to an exact message in the same conversation/topic. Requires separate human-enabled account-wide sending. Text is preserved and limited to 4096 UTF-16 units; previews are disabled. Use the operation_id from resolve_recipient or a canonical UUIDv7. Reuse the same ID and identical input after interruption; never automatically replace an uncertain ID. Dispatch is allowed for 24 hours from the ID timestamp. Known receipts remain available for seven days. A sent receipt means Telegram acceptance, not recipient delivery or reading. Errors can include a confirmed sent receipt; preserve it. Sending to bots can trigger bot behavior.",
			InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["peer","text","operation_id"],"properties":{"peer":{"type":"string","pattern":"` + peerPattern + `"},"text":{"type":"string","minLength":1,"maxLength":4096},"operation_id":{"type":"string","pattern":"` + operationPattern + `"},"reply_to":{"type":"string","pattern":"` + messagePattern + `"}}}`),
			OutputSchema: sendingOutputSchema(false),
			Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: &open},
		},
		{
			Name:         "get_send_status",
			Description:  "Inspect retained local facts for one operation_id without sending, retrying, fetching history or requiring Telegram readiness. Requires current human-enabled sending permission. Missing or pruned records are unavailable, never proof of not_sent. Records are retained for seven days from the UUIDv7 timestamp. A sent receipt means server acceptance, not recipient delivery or reading.",
			InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation_id"],"properties":{"operation_id":{"type":"string","pattern":"` + operationPattern + `"}}}`),
			OutputSchema: sendingOutputSchema(false),
			Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &open},
		},
	} {
		var schema jsonschema.Schema
		if err := json.Unmarshal(tool.InputSchema.(json.RawMessage), &schema); err != nil {
			panic("invalid static sending input schema")
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			panic("unresolvable static sending input schema")
		}
		server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := "req_" + rand.Text()
			args := request.Params.Arguments
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			var result sender.Result
			err := validateArguments(args, resolved)
			if !utf8.Valid(args) {
				err = model.TextError(model.ErrorInvalidInput, nil)
			}
			if err == nil {
				result, err = callSending(ctx, service, id, request.Params.Name, args)
			}
			if err != nil || result.IsError {
				logging.New(os.Stderr, nil).Warn(ctx, logging.EventOperationFailed, logging.ComponentField(logging.ComponentDaemon), logging.RequestIDField(id), logging.ErrorCategoryField(sendingErrorCategory(err)))
			}
			if len(result.JSON) == 0 {
				envelope, buildErr := sender.ErrorEnvelope(id, err)
				if buildErr != nil {
					return nil, errors.New("sending operation failed")
				}
				result.JSON, buildErr = json.Marshal(envelope)
				if buildErr != nil {
					return nil, errors.New("sending operation failed")
				}
				result.IsError = true
			}
			return &mcp.CallToolResult{
				IsError:           result.IsError || err != nil,
				StructuredContent: result.JSON,
				Content:           []mcp.Content{&mcp.TextContent{Text: string(result.JSON)}},
			}, nil
		})
	}
}

func sendingOutputSchema(recipient bool) json.RawMessage {
	item := `{"type":"object","additionalProperties":false,"required":["operation_id","peer","state"],"properties":{"operation_id":{"type":"string","pattern":"` + operationPattern + `"},"peer":{"type":"string","pattern":"` + peerPattern + `"},"state":{"enum":["not_sent","in_flight","sent","uncertain"]},"message_id":{"type":"string","pattern":"` + messagePattern + `"},"sent_at":{"type":"string","format":"date-time"},"failure_code":{"enum":["write_forbidden","payment_required","rate_limited","account_restricted","send_effect_uncertain","state_sync_failed","receipt_not_persisted","recipient_unavailable","invalid_reference","message_too_long"]},"retry_at":{"type":"string","format":"date-time"}}}`
	if recipient {
		item = `{"type":"object","additionalProperties":false,"required":["operation_id","peer","kind"],"properties":{"operation_id":{"type":"string","pattern":"` + operationPattern + `"},"peer":{"type":"string","pattern":"` + peerPattern + `"},"kind":{"enum":["self","user","chat","channel"]},"display_name":{"type":"string","maxLength":256},"username":{"type":"string","maxLength":32}}}`
	}
	errorBody := `{"type":"object","additionalProperties":false,"required":["category","message"],"properties":{"category":{"enum":["invalid_input","not_ready","reauth_required","policy_denied","consent_required","unsupported_peer","protected_content","ephemeral_content","invalid_reference","cursor_invalid","cursor_expired","resource_expired","rate_limited","freshness_degraded","partial_result","telegram_unavailable","result_too_large","media_too_large","transcription_pending","transcription_unavailable","transcription_failed","cancelled","read_effect_uncertain","internal","operation_conflict","operation_expired","operation_unavailable","recipient_unavailable","write_forbidden","payment_required","account_restricted","send_effect_uncertain","state_sync_failed","receipt_not_persisted"]},"message":{"type":"string","maxLength":512},"retry_after_seconds":{"type":"integer","minimum":1,"maximum":3600},"retry_at":{"type":"string","format":"date-time"}},"not":{"required":["retry_after_seconds","retry_at"]}}`
	return json.RawMessage(`{"type":"object","oneOf":[{"type":"object","additionalProperties":false,"required":["schema_version","request_id","freshness","partial","read_effect","items","next_cursor","warnings","untrusted_content"],"properties":{"schema_version":{"const":"1"},"request_id":{"type":"string","pattern":"^req_[A-Za-z0-9_-]+$","maxLength":128},"freshness":{"type":"object","additionalProperties":false,"required":["telegram","checked_at"],"properties":{"telegram":{"enum":["live","recovering","stale","partial","unavailable"]},"checked_at":{"type":"string","format":"date-time"}}},"partial":{"const":false},"read_effect":{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"const":"none"}}},"items":{"type":"array","minItems":1,"maxItems":1,"items":` + item + `},"next_cursor":{"type":"null"},"warnings":{"type":"array","maxItems":1,"uniqueItems":true,"items":{"const":"freshness_degraded"}},"untrusted_content":{"const":true},"error":` + errorBody + `}},{"type":"object","additionalProperties":false,"required":["schema_version","request_id","error"],"properties":{"schema_version":{"const":"1"},"request_id":{"type":"string","pattern":"^req_[A-Za-z0-9_-]+$","maxLength":128},"error":` + errorBody + `}}]}`)
}

func callSending(ctx context.Context, service *sender.Service, requestID, name string, args json.RawMessage) (sender.Result, error) {
	invalid := func() (sender.Result, error) { return sender.Result{}, model.TextError(model.ErrorInvalidInput, nil) }
	switch name {
	case "resolve_recipient":
		input, err := model.DecodeStrict[struct {
			Username string `json:"username"`
			Phone    string `json:"phone"`
			Peer     string `json:"peer"`
		}](args)
		if err != nil {
			return invalid()
		}
		query := model.ResolveRecipientQuery{Username: input.Username, Phone: input.Phone}
		if input.Peer != "" {
			query.Peer, err = model.ParsePeerID(input.Peer)
			if err != nil {
				return invalid()
			}
		}
		return service.ResolveRecipient(ctx, requestID, query)
	case "send_message":
		input, err := model.DecodeStrict[struct {
			Peer        string  `json:"peer"`
			Text        string  `json:"text"`
			OperationID string  `json:"operation_id"`
			ReplyTo     *string `json:"reply_to"`
		}](args)
		if err != nil {
			return invalid()
		}
		peer, err := model.ParsePeerID(input.Peer)
		if err != nil {
			return invalid()
		}
		request := model.SendRequest{Peer: peer, Text: input.Text, OperationID: input.OperationID}
		if input.ReplyTo != nil {
			reply, err := model.ParseMessageID(*input.ReplyTo)
			if err != nil {
				return invalid()
			}
			request.ReplyTo = &reply
		}
		return service.SendMessage(ctx, requestID, request)
	case "get_send_status":
		input, err := model.DecodeStrict[struct {
			OperationID string `json:"operation_id"`
		}](args)
		if err != nil {
			return invalid()
		}
		return service.SendStatus(ctx, requestID, input.OperationID)
	default:
		return invalid()
	}
}

func sendingErrorCategory(err error) model.ErrorCategory {
	if err == nil {
		return model.ErrorInternal
	}
	return model.TextErrorCategory(err)
}
