package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func catchUpTool(open *bool) *mcp.Tool {
	return &mcp.Tool{
		Name:         "catch_up",
		Description:  "Catch up on a named scope within [since, until), using explicit RFC3339 timestamps with a timezone and whole-second precision. Returns authorized snippets up to 240 characters and image/document/voice-note references without read receipts; open exact context, images, documents or voice notes separately. Repeat the same scope, dates and limit with next_cursor until null, including empty pages. Canonical peer order, newest IDs within each peer; not global chronological order or a snapshot. limit bounds fetched candidates including filtered entries (default 20, maximum 100), at most 20 peer lookups per page. scope.catch_up shows each eligible peer as pending, in_progress or complete, with fetched/returned counts for this page only. Complete means the authorized window was traversed, not that excluded content is absent. Never treat pending peers as empty. Completed windows ending no later than now return scope.catch_up.checkpoint, valid up to 30 days or grant expiry. Resume with checkpoint instead of since, plus a new explicit until. Keep the same checkpoint, until and limit through pagination. Save the new checkpoint only after consuming all pages; errors and unfinished or future windows do not advance it. Scope/policy/epoch changes invalidate checkpoints. This discovers newly sent messages, not edits or deletions of older messages.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["scope","until"],"oneOf":[{"required":["since"]},{"required":["checkpoint"]}],"properties":{"scope":{"type":"string","pattern":"` + scopePattern + `"},"checkpoint":{"type":"string","minLength":1,"maxLength":4096},"since":{"type":"string","minLength":20,"maxLength":35},"until":{"type":"string","minLength":20,"maxLength":35},"limit":{"type":"integer","minimum":1,"maximum":100},"cursor":{"type":"string","minLength":1,"maxLength":4096}}}`),
		OutputSchema: textOutputSchema("catch_up"),
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: open},
	}
}

func callCatchUp(ctx context.Context, service *reader.Service, id string, args json.RawMessage) (reader.Result, error) {
	invalid := func() (reader.Result, error) { return reader.Result{}, model.TextError(model.ErrorInvalidInput, nil) }
	input, err := model.DecodeStrict[struct {
		Scope      string  `json:"scope"`
		Since      *string `json:"since"`
		Checkpoint *string `json:"checkpoint"`
		Until      string  `json:"until"`
		Limit      *int    `json:"limit"`
		Cursor     *string `json:"cursor"`
	}](args)
	if err != nil {
		return invalid()
	}
	scope, err := model.ParseScopeID(input.Scope)
	if err != nil {
		return invalid()
	}
	if (input.Since == nil) == (input.Checkpoint == nil) {
		return invalid()
	}
	until, err := model.ParseSearchDate(input.Until)
	if err != nil {
		return invalid()
	}
	var window model.DateWindow
	if input.Since != nil {
		window, err = model.ParseDateWindow(*input.Since, input.Until)
		if err != nil {
			return invalid()
		}
	} else if *input.Checkpoint == "" || len(*input.Checkpoint) > 4096 {
		return invalid()
	}
	limit := model.DefaultPageSize
	if input.Limit != nil {
		limit = *input.Limit
	}
	if model.ValidatePageSize(limit) != nil {
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
	if input.Checkpoint != nil {
		return service.CatchUpFrom(ctx, id, scope, *input.Checkpoint, until, limit, token)
	}
	return service.CatchUp(ctx, id, scope, window, limit, token)
}
