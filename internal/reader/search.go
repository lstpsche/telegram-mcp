package reader

import (
	"context"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func (s *Service) Search(ctx context.Context, requestID string, peer model.PeerID, query string, limit int, token string) (result Result, resultErr error) {
	query, err := model.NormalizeSearchQuery(query)
	if err != nil {
		return Result{}, err
	}
	if peer.String() == "" || model.ValidatePageSize(limit) != nil || len(token) > 4096 {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	lease, err := s.policy.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	count := 0
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, "search_messages", count, false, resultErr)
		if resultErr != nil {
			result = Result{}
		}
	}()
	grant, err := lease.Grant(ctx, peer)
	if err != nil {
		return Result{}, err
	}
	if grant.Profile == policy.ProfileSelfAuthored && grant.Author != s.backend.SelfID() {
		return Result{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	ctx, expires := context.WithTimeout(ctx, grant.ExpiresAt.Sub(s.now()))
	defer expires()
	epoch, revision, err := lease.Binding(ctx)
	if err != nil {
		return Result{}, err
	}
	binding := cursorBinding{Operation: "search_messages", Peer: peer, QueryDigest: s.queryDigest(query), Limit: limit, Epoch: epoch, Revision: revision}
	deadline := s.now().Add(cursorLifetime)
	if grant.ExpiresAt.Before(deadline) {
		deadline = grant.ExpiresAt
	}
	cursor := searchCursor{Binding: binding, Ceiling: grant.MaxID, Expires: deadline.Unix()}
	if token != "" {
		cursor, err = s.decodeCursor(token, binding)
		if err != nil {
			return Result{}, err
		}
		if cursor.Before <= grant.MinID || cursor.Ceiling > grant.MaxID || cursor.Expires > grant.ExpiresAt.Unix() {
			return Result{}, model.TextError(model.ErrorCursorInvalid, nil)
		}
	}
	ctx, stopCursor := context.WithTimeout(ctx, time.Unix(cursor.Expires, 0).Sub(s.now()))
	defer stopCursor()
	candidates, err := s.backend.Search(ctx, model.SearchQuery{Peer: peer, Query: query, MinID: grant.MinID, MaxID: cursor.Ceiling, Before: cursor.Before, Limit: limit})
	if err != nil {
		return Result{}, err
	}
	if len(candidates) > limit {
		return Result{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	items := make([]model.SearchHit, 0, len(candidates))
	seen := make(map[int32]bool, len(candidates))
	partial := len(candidates) == limit
	var lowest, highest int32
	for _, candidate := range candidates {
		message := candidate.Message
		id := message.ID.TelegramID()
		if message.ID.Peer() != peer || id < grant.MinID || id > cursor.Ceiling || (cursor.Before > 0 && id >= cursor.Before) || seen[id] {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		seen[id] = true
		if lowest == 0 || id < lowest {
			lowest = id
		}
		if id > highest {
			highest = id
		}
		if err := grant.CheckMessage(candidate, s.backend.SelfID(), s.now()); err != nil {
			switch model.TextErrorCategory(err) {
			case model.ErrorPolicyDenied, model.ErrorProtectedContent, model.ErrorEphemeralContent, model.ErrorUnsupportedPeer:
				partial = true
				continue
			default:
				return Result{}, err
			}
		}
		date, err := time.Parse(time.RFC3339Nano, message.Date)
		if err != nil || date.IsZero() || message.Author.Kind() != model.PeerKindUser || message.Text == "" || !utf8.ValidString(message.Text) {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if len(message.Text) > 64*1024 {
			return Result{}, model.TextError(model.ErrorResultTooLarge, nil)
		}
		snippet := []rune(message.Text)
		truncated := len(snippet) > 240
		if truncated {
			snippet = snippet[:240]
		}
		items = append(items, model.SearchHit{ID: message.ID, Author: message.Author, Date: date.UTC().Format(time.RFC3339Nano), Snippet: string(snippet), SnippetTruncated: truncated})
	}
	if err := grant.CheckCurrent(s.now()); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if cursor.Expires <= s.now().Unix() {
		return Result{}, model.TextError(model.ErrorCursorExpired, nil)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID.TelegramID() > items[j].ID.TelegramID() })
	var next *string
	if len(candidates) == limit && lowest > grant.MinID {
		if cursor.Before == 0 {
			cursor.Ceiling = highest
		}
		cursor.Before = lowest
		encoded, err := s.encodeCursor(cursor)
		if err != nil {
			return Result{}, err
		}
		next = &encoded
	}
	result, err = prepare(requestID, items, s.now(), partial, model.NoReadEffect(), next)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}

// ListUnread returns whole-dialog counts for the bounded set of current grants.
// It never substitutes a partial result when any required peer lookup fails.
func (s *Service) ListUnread(ctx context.Context, requestID string) (result Result, resultErr error) {
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	lease, err := s.policy.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	count := 0
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, "list_unread", count, false, resultErr)
		if resultErr != nil {
			result = Result{}
		}
	}()
	grants, err := lease.List(ctx)
	if err != nil {
		return Result{}, err
	}
	items := make([]model.Unread, 0, len(grants))
	for _, grant := range grants {
		if err := grant.CheckCurrent(s.now()); err != nil {
			return Result{}, err
		}
		bounded, stop := context.WithTimeout(ctx, grant.ExpiresAt.Sub(s.now()))
		unread, err := s.backend.Unread(bounded, grant.Peer)
		stop()
		if err != nil {
			return Result{}, err
		}
		if unread.Peer != grant.Peer || unread.Count < 0 || unread.Count > 2147483647 {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if unread.Count > 0 || unread.Marked {
			items = append(items, unread)
		}
	}
	for _, grant := range grants {
		if err := grant.CheckCurrent(s.now()); err != nil {
			return Result{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	result, err = prepare(requestID, items, s.now(), false, model.NoReadEffect(), nil)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}
