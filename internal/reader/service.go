// Package reader coordinates authorization, bounded text assembly and Telegram
// read acknowledgment. Only prepared, authorized results leave this boundary.
package reader

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

const OperationTimeout = 20 * time.Second

// Backend is the normalized external Telegram I/O seam.
type Backend interface {
	Ready() bool
	SelfID() model.PeerID
	Chat(context.Context, model.PeerID) (model.Chat, error)
	History(context.Context, model.HistoryQuery) ([]model.Candidate, error)
	Acknowledge(context.Context, model.PeerID, int32) error
	Search(context.Context, model.SearchQuery) ([]model.Candidate, error)
	Unread(context.Context, model.PeerID) (model.Unread, error)
}

// Result is serialized before the upstream effect. JSON is also used verbatim
// as the structured result, so both MCP content representations agree.
type Result struct{ JSON json.RawMessage }

type Service struct {
	backend   Backend
	policy    *policy.Repository
	now       func() time.Time
	cursorKey [32]byte
}

func New(backend Backend, repository *policy.Repository, now func() time.Time, cursorKey []byte) (*Service, error) {
	if backend == nil || repository == nil || now == nil || len(cursorKey) != 32 {
		return nil, errors.New("text service dependencies are required")
	}
	s := &Service{backend: backend, policy: repository, now: now}
	copy(s.cursorKey[:], cursorKey)
	return s, nil
}

func (s *Service) Ready() bool { return s != nil && s.backend.Ready() }

func (s *Service) ListChats(ctx context.Context, requestID string, limit int) (result Result, resultErr error) {
	if err := model.ValidatePageSize(limit); err != nil {
		return Result{}, model.TextError(model.ErrorInvalidInput, err)
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
		resultErr = s.finish(ctx, lease, requestID, "list_chats", count, false, resultErr)
		if resultErr != nil {
			result = Result{}
		}
	}()
	grants, err := lease.List(ctx)
	if err != nil {
		return Result{}, err
	}
	items := make([]model.Chat, 0, len(grants))
	partial := len(grants) > limit
	if partial {
		grants = grants[:limit]
	}
	for _, grant := range grants {
		if !grant.Eligible || !grant.ExpiresAt.After(s.now()) {
			return Result{}, model.TextError(model.ErrorConsentRequired, nil)
		}
		grantContext, stop := context.WithTimeout(ctx, grant.ExpiresAt.Sub(s.now()))
		chat, err := s.backend.Chat(grantContext, grant.Peer)
		stop()
		if err != nil {
			return Result{}, err
		}
		if chat.ID != grant.Peer {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		items = append(items, chat)
	}
	for _, grant := range grants {
		if !grant.ExpiresAt.After(s.now()) {
			return Result{}, model.TextError(model.ErrorConsentRequired, nil)
		}
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	result, err = prepare(requestID, items, s.now(), partial, model.NoReadEffect(), nil)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}

func (s *Service) Messages(ctx context.Context, requestID string, query model.HistoryQuery) (result Result, resultErr error) {
	if query.Peer.String() == "" || query.Before < 0 || query.Target < 0 || query.BeforeCount < 0 || query.AfterCount < 0 || query.BeforeCount > 49 || query.AfterCount > 49 || (query.Target > 0 && query.Before != 0) {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if err := model.ValidatePageSize(query.Limit); err != nil {
		return Result{}, model.TextError(model.ErrorInvalidInput, err)
	}
	if query.Target > 0 && query.Limit != query.BeforeCount+query.AfterCount+1 {
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
	operation := "list_messages"
	if query.Target > 0 {
		operation = "get_message_context"
	}
	count := 0
	attemptedAck := false
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, operation, count, attemptedAck, resultErr)
		if resultErr != nil {
			result = Result{}
		}
	}()
	grant, err := lease.Grant(ctx, query.Peer)
	if err != nil {
		return Result{}, err
	}
	ctx, expires := context.WithTimeout(ctx, grant.ExpiresAt.Sub(s.now()))
	defer expires()
	if query.Target > 0 && (query.Target < grant.MinID || query.Target > grant.MaxID) {
		return Result{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	query.MinID = grant.MinID
	query.MaxID = grant.MaxID
	if query.Before > 0 && query.Before <= grant.MinID {
		return Result{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	candidates, err := s.backend.History(ctx, query)
	if err != nil {
		return Result{}, err
	}
	if err := grant.CheckCurrent(s.now()); err != nil {
		return Result{}, err
	}
	if len(candidates) > query.Limit {
		return Result{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	items := make([]model.Message, 0, len(candidates))
	seen := make(map[model.MessageID]bool, len(candidates))
	partial := len(candidates) == query.Limit
	found := query.Target == 0
	var through int32
	older, newer := 0, 0
	for _, candidate := range candidates {
		message := candidate.Message
		if message.ID.Peer() != query.Peer || message.ID.String() == "" || seen[message.ID] {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		seen[message.ID] = true
		if query.Target > 0 {
			if message.ID.TelegramID() < query.Target {
				older++
			}
			if message.ID.TelegramID() > query.Target {
				newer++
			}
			if older > query.BeforeCount || newer > query.AfterCount {
				return Result{}, model.TextError(model.ErrorInvalidReference, nil)
			}
		}
		if query.Before > 0 && message.ID.TelegramID() >= query.Before {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if err := grant.CheckMessage(candidate, s.backend.SelfID(), s.now()); err != nil {
			category := model.TextErrorCategory(err)
			if category != model.ErrorPolicyDenied && category != model.ErrorProtectedContent && category != model.ErrorEphemeralContent && category != model.ErrorUnsupportedPeer {
				return Result{}, err
			}
			partial = true
			continue
		}
		if message.Author.String() == "" {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		date, err := time.Parse(time.RFC3339Nano, message.Date)
		if err != nil || date.IsZero() {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if message.ID.TelegramID() == query.Target {
			found = true
		}
		if message.ID.TelegramID() > through {
			through = message.ID.TelegramID()
		}
		items = append(items, message)
	}
	if !found {
		return Result{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	if err := grant.CheckCurrent(s.now()); err != nil {
		return Result{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID.TelegramID() > items[j].ID.TelegramID() })
	effect := model.NoReadEffect()
	if through > 0 {
		if err := grant.CheckRead(through, s.now()); err != nil {
			return Result{}, err
		}
		boundary, err := model.NewMessageID(query.Peer, through)
		if err != nil {
			return Result{}, err
		}
		effect = model.ReadEffect{Kind: model.ReadEffectHistoryMarkedRead, ThroughMessageID: &boundary}
	}
	result, err = prepare(requestID, items, s.now(), partial, effect, nil)
	if err != nil {
		return Result{}, err
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if through > 0 {
		// The lease excludes revocation; expiration is checked immediately before I/O.
		if err := grant.CheckRead(through, s.now()); err != nil {
			return Result{}, err
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		attemptedAck = true
		if err := s.backend.Acknowledge(ctx, query.Peer, through); err != nil {
			return Result{}, model.TextError(model.ErrorReadEffectUncertain, err)
		}
		if err := grant.CheckCurrent(s.now()); err != nil {
			return Result{}, model.TextError(model.ErrorReadEffectUncertain, err)
		}
		if err := ctx.Err(); err != nil {
			return Result{}, model.TextError(model.ErrorReadEffectUncertain, err)
		}
		if !s.Ready() {
			return Result{}, model.TextError(model.ErrorReadEffectUncertain, nil)
		}
	}
	count = len(items)
	return result, nil
}

func prepare[T any](requestID string, items []T, now time.Time, partial bool, effect model.ReadEffect, next *string) (Result, error) {
	freshness, err := model.NewFreshness(model.FreshnessLive, now)
	if err != nil {
		return Result{}, err
	}
	envelope, err := model.NewEnvelope(requestID, freshness, items)
	if err != nil {
		return Result{}, err
	}
	envelope.Partial = partial
	envelope.ReadEffect = effect
	envelope.NextCursor = next
	if partial {
		envelope.Warnings = append(envelope.Warnings, model.WarningPartialResult)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return Result{}, model.TextError(model.ErrorResultTooLarge, err)
	}
	mirror, err := json.Marshal(string(encoded))
	if err != nil {
		return Result{}, err
	}
	// Ingress bounds the normalized JSON-RPC ID, including Unicode escaping.
	// Both structured and escaped text mirrors and framing fields are counted.
	if len(encoded)+len(mirror)+model.MaximumRPCIDBytes+1024 > model.MaximumTextResultBytes {
		return Result{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	return Result{JSON: encoded}, nil
}

func (s *Service) finish(ctx context.Context, lease *policy.Lease, id, operation string, count int, attempted bool, err error) error {
	// Audit is metadata-only and still attempted when the caller disconnected.
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err != nil {
		count = 0
	}
	auditErr := lease.Audit(auditContext, id, operation, model.TextErrorCategory(err), count, attempted && err != nil)
	closeErr := lease.Close()
	joined := errors.Join(err, auditErr, closeErr)
	if attempted && joined != nil {
		return model.TextError(model.ErrorReadEffectUncertain, joined)
	}
	return joined
}
