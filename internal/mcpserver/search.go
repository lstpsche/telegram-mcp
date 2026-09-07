package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/reader"
)

func singleSearchInputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"allOf":[{"oneOf":[{"required":["peer"]},{"required":["scope"]}]},{"anyOf":[{"required":["query"]},{"required":["unread_mentions_only"],"properties":{"unread_mentions_only":{"const":true}}},{"required":["pinned_only"],"properties":{"pinned_only":{"const":true}}},{"required":["media_type"]},{"required":["sender"]},{"required":["since"]},{"required":["until"]},{"required":["saved_peer"]},{"required":["saved_tag"]},{"required":["reply_to"]},{"required":["thread_root"]}]}],"properties":{"scope":{"type":"string","pattern":"` + scopePattern + `"},"peer":{"type":"string","pattern":"` + peerPattern + `"},"saved_peer":{"type":"string","pattern":"^tgpeer:v1:(self|user|chat|channel):[1-9][0-9]*$"},"reply_to":{"type":"string","pattern":"` + messagePattern + `"},"thread_root":{"type":"string","pattern":"` + messagePattern + `"},"saved_tag":` + savedTagSchema + `,"sender":{"type":"string","pattern":"^tgpeer:v1:(user|channel):[1-9][0-9]*$"},"since":{"type":"string","minLength":20,"maxLength":25},"until":{"type":"string","minLength":20,"maxLength":25},"unread_mentions_only":{"type":"boolean"},"pinned_only":{"type":"boolean"},"query":{"type":"string","maxLength":1024},"limit":{"type":"integer","minimum":1,"maximum":100},"cursor":{"type":"string","minLength":1,"maxLength":4096},"media_type":{"type":"string","enum":["photo","image_file","pdf","text_file","voice_note"]}}}`)
}

func batchSearchInputSchema() json.RawMessage {
	single := singleSearchInputSchema()
	return json.RawMessage(`{"type":"object","oneOf":[` + string(single) + `,{"type":"object","additionalProperties":false,"required":["searches"],"properties":{"searches":{"type":"array","minItems":1,"maxItems":10,"items":` + string(single) + `}}}]}`)
}

func parseSearchRequest(args json.RawMessage) (model.SearchRequest, error) {
	limit := model.DefaultPageSize
	input, err := model.DecodeStrict[struct {
		ReplyTo            string                `json:"reply_to"`
		ThreadRoot         string                `json:"thread_root"`
		SavedPeer          string                `json:"saved_peer"`
		SavedTag           *model.SavedTag       `json:"saved_tag"`
		Sender             string                `json:"sender"`
		Since              *string               `json:"since"`
		Until              *string               `json:"until"`
		Peer               *string               `json:"peer"`
		Scope              *string               `json:"scope"`
		UnreadMentionsOnly bool                  `json:"unread_mentions_only"`
		PinnedOnly         bool                  `json:"pinned_only"`
		MediaType          model.SearchMediaType `json:"media_type"`
		Query              string                `json:"query"`
		Limit              *int                  `json:"limit"`
		Cursor             *string               `json:"cursor"`
	}](args)
	if err != nil {
		return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if (input.Peer == nil) == (input.Scope == nil) {
		return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	var peer model.PeerID
	var scopes []model.ScopeID
	if input.Peer != nil {
		peer, err = model.ParsePeerID(*input.Peer)
	} else {
		scopes, err = parseScopeSelector(input.Scope)
	}
	if err != nil {
		return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if input.Limit != nil {
		limit = *input.Limit
	}
	filter := model.SearchFilter{ReplyTo: input.ReplyTo, ThreadRoot: input.ThreadRoot, SavedPeer: input.SavedPeer, Sender: input.Sender, MediaType: input.MediaType, Query: input.Query, UnreadMentionsOnly: input.UnreadMentionsOnly, PinnedOnly: input.PinnedOnly}
	if input.SavedTag != nil {
		if !input.SavedTag.Valid() {
			return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
		}
		filter.SavedTag = *input.SavedTag
	}
	if filter.CheckReplyPeer(peer) != nil || filter.HasSavedFilter() && (len(scopes) != 0 || peer.Kind() != model.PeerKindSelf) {
		return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if input.Since != nil {
		filter.Since, err = model.ParseSearchDate(*input.Since)
		if err != nil {
			return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
		}
	}
	if input.Until != nil {
		filter.Until, err = model.ParseSearchDate(*input.Until)
		if err != nil {
			return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
		}
	}
	filter, err = filter.Normalize()
	if err != nil || model.ValidatePageSize(limit) != nil {
		return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	token := ""
	if input.Cursor != nil {
		token = *input.Cursor
		if token == "" || len(token) > 4096 {
			return model.SearchRequest{}, model.TextError(model.ErrorInvalidInput, nil)
		}
	}

	request := model.SearchRequest{Peer: peer, Filter: filter, Limit: limit, Cursor: token}
	if len(scopes) == 1 {
		request.Scope = scopes[0]
	}
	return request, nil
}

func callSearch(ctx context.Context, service *reader.Service, id string, args json.RawMessage) (reader.Result, error) {
	var selector map[string]json.RawMessage
	if err := json.Unmarshal(args, &selector); err != nil {
		return reader.Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if raw, ok := selector["searches"]; ok {
		if len(selector) != 1 {
			return reader.Result{}, model.TextError(model.ErrorInvalidInput, nil)
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 || len(entries) > model.MaximumSearchRequests {
			return reader.Result{}, model.TextError(model.ErrorInvalidInput, nil)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(singleSearchInputSchema(), &schema); err != nil {
			return reader.Result{}, err
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return reader.Result{}, err
		}
		requests := make([]model.SearchRequest, 0, len(entries))
		for _, entry := range entries {
			if err := validateArguments(entry, resolved); err != nil {
				return reader.Result{}, err
			}
			request, err := parseSearchRequest(entry)
			if err != nil {
				return reader.Result{}, err
			}
			requests = append(requests, request)
		}
		if service == nil {
			return reader.Result{}, model.TextError(model.ErrorNotReady, nil)
		}
		return service.Searches(ctx, id, requests)
	}
	request, err := parseSearchRequest(args)
	if err != nil {
		return reader.Result{}, err
	}
	if service == nil {
		return reader.Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	if request.Scope.String() != "" {
		return service.SearchScope(ctx, id, request.Scope, request.Filter, request.Limit, request.Cursor)
	}
	return service.Search(ctx, id, request.Peer, request.Filter, request.Limit, request.Cursor)
}
