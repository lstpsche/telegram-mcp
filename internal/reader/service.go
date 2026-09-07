// Package reader coordinates authorization, bounded text assembly and Telegram
// read acknowledgment. Only prepared, authorized results leave this boundary.
package reader

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
type Result struct {
	JSON     json.RawMessage
	Image    *MediaContent
	Voice    *MediaContent
	Document *DocumentContent
}

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

func (s *Service) ListChats(ctx context.Context, requestID string, limit int, scopes ...model.ScopeID) (Result, error) {
	return s.Chats(ctx, requestID, limit, scopes, "")
}

func (s *Service) Chats(ctx context.Context, requestID string, limit int, scopes []model.ScopeID, token string) (result Result, resultErr error) {
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
	var dialogExpiry int64
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, "list_chats", count, false, resultErr, func() error {
			return s.checkDialogRelease(ctx, dialogExpiry)
		})
		if resultErr != nil {
			result = Result{}
		}
	}()
	full, err := lease.FullRead(ctx)
	if err != nil {
		return Result{}, err
	}
	if full && len(scopes) == 0 {
		result, count, err = s.fullDialogs(ctx, lease, requestID, "list_chats", limit, token, &dialogExpiry)
		return result, err
	}
	if token != "" {
		return Result{}, model.TextError(model.ErrorCursorInvalid, nil)
	}
	grants, coverage, err := s.selectGrants(ctx, lease, scopes)
	if err != nil {
		return Result{}, err
	}
	selected := grants
	items := make([]model.Chat, 0, len(grants))
	partial := len(grants) > limit
	if partial {
		grants = grants[:limit]
	}
	for _, grant := range grants {
		if err := s.checkGrantsCurrent(ctx, selected); err != nil {
			return Result{}, err
		}
		grantContext, stop := context.WithTimeout(ctx, grant.Deadline(s.now().Add(OperationTimeout)).Sub(s.now()))
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
	if err := s.checkGrantsCurrent(ctx, selected); err != nil {
		return Result{}, err
	}
	if coverage != nil {
		coverage.QueriedPeers = len(grants)
		coverage.CompletedPeers = len(grants)
		partial = partial || coverage.ExcludedPeers > 0
	}
	result, err = prepare(requestID, items, s.now(), partial, model.NoReadEffect(), nil, coverage)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}

func validateHistory(query model.HistoryQuery) error {
	if query.LinkUsername != "" {
		link, err := model.ParseMessageLink("https://t.me/" + query.LinkUsername + "/1")
		if err != nil || link.Username != query.LinkUsername || query.Peer.String() != "" || query.Target <= 0 || query.LinkTopic < 0 || query.LinkTopic > query.Target {
			return model.TextError(model.ErrorInvalidInput, nil)
		}
	} else if query.LinkTopic != 0 {
		return model.TextError(model.ErrorInvalidInput, nil)
	}
	if (query.Peer.String() == "" && query.LinkUsername == "") || query.Before < 0 || query.Target < 0 || query.BeforeCount < 0 || query.AfterCount < 0 || query.BeforeCount > 49 || query.AfterCount > 49 || (query.Target > 0 && query.Before != 0) {
		return model.TextError(model.ErrorInvalidInput, nil)
	}
	if err := model.ValidatePageSize(query.Limit); err != nil {
		return model.TextError(model.ErrorInvalidInput, err)
	}
	if query.ResolveDiscussion && (query.Target == 0 || (query.Peer.Kind() != model.PeerKindChannel && query.LinkUsername == "") || query.Peer.TopicID() != 0 || query.LinkTopic != 0) {
		return model.TextError(model.ErrorInvalidInput, nil)
	}
	if query.ReplyDepth < 0 || query.ReplyDepth > 5 || (query.ReplyDepth > 0 && query.Target == 0) || query.Limit+query.ReplyDepth > model.MaximumPageSize {
		return model.TextError(model.ErrorInvalidInput, nil)
	}
	if query.Target > 0 && query.Limit != query.BeforeCount+query.AfterCount+1 {
		return model.TextError(model.ErrorInvalidInput, nil)
	}
	return nil
}

func (s *Service) Messages(ctx context.Context, requestID string, query model.HistoryQuery) (Result, error) {
	return s.messages(ctx, requestID, []model.HistoryQuery{query}, false)
}

// Contexts prepares every target before issuing any conversation receipt.
func (s *Service) Contexts(ctx context.Context, requestID string, queries []model.HistoryQuery) (Result, error) {
	return s.messages(ctx, requestID, queries, true)
}

type preparedContext struct {
	items   []model.Message
	partial bool
	through int32
}

func (s *Service) collectContext(ctx context.Context, lease *policy.Lease, query model.HistoryQuery, grant policy.Grant, authority mediaAuthority) (preparedContext, error) {
	query.MinID = grant.MinID
	query.MaxID = grant.MaxID
	if query.Before > 0 && query.Before <= grant.MinID {
		return preparedContext{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	candidates, err := s.backend.History(ctx, query)
	if err != nil {
		return preparedContext{}, err
	}
	if err := grant.CheckCurrent(s.now()); err != nil {
		return preparedContext{}, err
	}
	if len(candidates) > query.Limit {
		return preparedContext{}, model.TextError(model.ErrorResultTooLarge, nil)
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
			return preparedContext{}, model.TextError(model.ErrorInvalidReference, nil)
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
				return preparedContext{}, model.TextError(model.ErrorInvalidReference, nil)
			}
		}
		if query.Before > 0 && message.ID.TelegramID() >= query.Before {
			return preparedContext{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if err := grant.CheckMessage(candidate, s.backend.SelfID(), s.now()); err != nil {
			category := model.TextErrorCategory(err)
			if category != model.ErrorPolicyDenied && category != model.ErrorProtectedContent && category != model.ErrorEphemeralContent && category != model.ErrorUnsupportedPeer {
				return preparedContext{}, err
			}
			partial = true
			continue
		}
		if message.Author.String() == "" {
			return preparedContext{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		message, err = s.prepareMessage(candidate, grant, authority)
		if err != nil {
			return preparedContext{}, err
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
		return preparedContext{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	if err := grant.CheckCurrent(s.now()); err != nil {
		return preparedContext{}, err
	}
	if query.ReplyDepth > 0 {
		if err := grant.CheckRead(through, s.now()); err != nil {
			return preparedContext{}, err
		}
		items, err = s.replyChain(ctx, query, grant, authority, items, seen)
		if err != nil {
			return preparedContext{}, err
		}
		for _, item := range items {
			if item.ReplyChain != nil && item.ReplyChain.State != "complete" {
				partial = true
			}
		}
	}

	if query.ResolveDiscussion {
		for i := range items {
			if items[i].ID.TelegramID() == query.Target {
				items[i].DiscussionRoot, err = s.resolveDiscussion(ctx, lease, grant, items[i])
				if err != nil {
					return preparedContext{}, err
				}
			}
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ID.TelegramID() > items[j].ID.TelegramID() })
	return preparedContext{items: items, partial: partial, through: through}, nil
}

func (s *Service) messages(ctx context.Context, requestID string, queries []model.HistoryQuery, batch bool) (result Result, resultErr error) {
	if len(queries) == 0 || len(queries) > model.MaximumContextTargets {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	total := 0
	targets := map[model.MessageID]bool{}
	for _, q := range queries {
		if err := validateHistory(q); err != nil {
			return Result{}, err
		}
		if batch && q.LinkUsername == "" {
			if q.Target == 0 {
				return Result{}, model.TextError(model.ErrorInvalidInput, nil)
			}
			id, err := model.NewMessageID(q.Peer, q.Target)
			if err != nil || targets[id] {
				return Result{}, model.TextError(model.ErrorInvalidInput, nil)
			}
			targets[id] = true
		}
		total += q.Limit + q.ReplyDepth
	}
	if total > model.MaximumPageSize {
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
	operation := "get_message_context"
	if !batch && queries[0].Target == 0 {
		operation = "list_messages"
	}
	count := 0
	attempted := false
	grants := make([]policy.Grant, 0, len(queries))
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, operation, count, attempted, resultErr, func() error { return s.checkGrantsCurrent(ctx, grants) })
		if resultErr != nil {
			result = Result{}
		}
	}()
	deadline := s.now().Add(OperationTimeout)
	for i, q := range queries {
		if q.LinkUsername != "" {
			q, err = s.resolveContextPublisher(ctx, lease, q)
			if err != nil {
				return Result{}, err
			}
			queries[i] = q
		}
		if q.ResolveDiscussion {
			full, err := lease.FullRead(ctx)
			if err != nil {
				return Result{}, err
			}
			if !full {
				return Result{}, model.TextError(model.ErrorPolicyDenied, nil)
			}
		}
		grant, err := lease.Grant(ctx, q.Peer)
		if err != nil {
			return Result{}, err
		}
		if q.Target > 0 && (q.Target < grant.MinID || q.Target > grant.MaxID) {
			return Result{}, model.TextError(model.ErrorPolicyDenied, nil)
		}
		grants = append(grants, grant)
		deadline = grant.Deadline(deadline)
	}
	if batch {
		unique := map[model.MessageID]bool{}
		for _, q := range queries {
			id, err := model.NewMessageID(q.Peer, q.Target)
			if err != nil || unique[id] {
				return Result{}, model.TextError(model.ErrorInvalidInput, nil)
			}
			unique[id] = true
		}
	}
	fetchContext, stop := context.WithTimeout(ctx, deadline.Sub(s.now()))
	defer stop()
	epoch, revision, err := lease.Binding(fetchContext)
	if err != nil {
		return Result{}, err
	}
	items := []model.Message{}
	contexts := []model.MessageContext{}
	seen := map[model.MessageID]int{}
	boundaries := map[model.PeerID]int32{}
	partial := false
	for i, q := range queries {
		if err := s.checkGrantsCurrent(fetchContext, grants); err != nil {
			return Result{}, err
		}
		prepared, err := s.collectContext(fetchContext, lease, q, grants[i], mediaAuthority{epoch, revision})
		if err != nil {
			return Result{}, err
		}
		partial = partial || prepared.partial
		if prepared.through > boundaries[q.Peer] {
			boundaries[q.Peer] = prepared.through
		}
		refs := make([]model.MessageID, 0, len(prepared.items))
		for _, item := range prepared.items {
			refs = append(refs, item.ID)
			if index, exists := seen[item.ID]; exists {
				merged, err := mergeContextMessage(items[index], item)
				if err != nil {
					return Result{}, err
				}
				items[index] = merged
			} else {
				seen[item.ID] = len(items)
				items = append(items, item)
			}
		}
		if batch {
			target, _ := model.NewMessageID(q.Peer, q.Target)
			contexts = append(contexts, model.MessageContext{Target: target, Messages: refs, Partial: prepared.partial})
		}
	}
	// Stable peer ordering gives deterministic receipts and cross-conversation output.
	peers := make([]model.PeerID, 0, len(boundaries))
	for peer := range boundaries {
		peers = append(peers, peer)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].String() < peers[j].String() })
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID.Peer() != items[j].ID.Peer() {
			return items[i].ID.Peer().String() < items[j].ID.Peer().String()
		}
		return items[i].ID.TelegramID() > items[j].ID.TelegramID()
	})
	receipts := make([]model.MessageID, 0, len(peers))
	for _, peer := range peers {
		for _, grant := range grants {
			if grant.Peer == peer {
				if err := grant.CheckRead(boundaries[peer], s.now()); err != nil {
					return Result{}, err
				}
				break
			}
		}
		id, err := model.NewMessageID(peer, boundaries[peer])
		if err != nil {
			return Result{}, err
		}
		receipts = append(receipts, id)
	}
	effect := model.NoReadEffect()
	if len(receipts) > 0 {
		effect.Kind = model.ReadEffectHistoryMarkedRead
		if batch {
			effect.ThroughMessageIDs = receipts
		} else {
			effect.ThroughMessageID = &receipts[0]
		}
	}
	result, err = prepare(requestID, items, s.now(), partial, effect, nil, nil, contexts...)
	if err != nil {
		return Result{}, err
	}

	for _, peer := range peers {
		if err := s.checkGrantsCurrent(fetchContext, grants); err != nil {
			return Result{}, err
		}
		attempted = true
		if err := s.backend.Acknowledge(fetchContext, peer, boundaries[peer]); err != nil {
			return Result{}, model.TextError(model.ErrorReadEffectUncertain, err)
		}
	}
	if err := s.checkGrantsCurrent(fetchContext, grants); err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}

func prepare[T any](requestID string, items []T, now time.Time, partial bool, effect model.ReadEffect, next *string, coverage *model.ScopeCoverage, contexts ...model.MessageContext) (Result, error) {
	state := model.FreshnessLive
	if coverage != nil && coverage.QueriedPeers == 0 {
		state = model.FreshnessUnavailable
	}
	freshness, err := model.NewFreshness(state, now)
	if err != nil {
		return Result{}, err
	}
	envelope, err := model.NewEnvelope(requestID, freshness, items)
	if err != nil {
		return Result{}, err
	}
	envelope.Contexts = contexts
	envelope.Scope = coverage
	envelope.Partial = partial
	envelope.ReadEffect = effect
	envelope.NextCursor = next
	if partial {
		envelope.Warnings = append(envelope.Warnings, model.WarningPartialResult)
	}
	return serializeEnvelope(envelope)
}

func serializeEnvelope[T any](envelope model.Envelope[T]) (Result, error) {
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

func (s *Service) finish(ctx context.Context, lease *policy.Lease, id, operation string, count int, attempted bool, err error, beforeRelease ...func() error) error {
	// Audit is metadata-only and still attempted when the caller disconnected.
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err != nil {
		count = 0
	}
	auditErr := lease.Audit(auditContext, id, operation, model.TextErrorCategory(err), count, attempted && err != nil)
	var releaseErr error
	if err == nil && auditErr == nil {
		for _, check := range beforeRelease {
			releaseErr = errors.Join(releaseErr, check())
		}
	}
	closeErr := lease.Close()
	joined := errors.Join(err, auditErr, releaseErr, closeErr)
	if attempted && joined != nil {
		return model.TextError(model.ErrorReadEffectUncertain, joined)
	}
	return joined
}

func (s *Service) prepareMessage(candidate model.Candidate, grant policy.Grant, authority mediaAuthority) (model.Message, error) {
	message := candidate.Message
	message.URL = message.ID.URL()
	date, err := time.Parse(time.RFC3339Nano, message.Date)
	if err != nil || date.IsZero() {
		return model.Message{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	message.Image, err = s.imageDescriptor(candidate, grant, authority)
	if err != nil {
		return model.Message{}, err
	}
	message.Document, err = s.documentDescriptor(candidate, grant, authority)
	if err != nil {
		return model.Message{}, err
	}

	message.Voice, err = s.voiceDescriptor(candidate, grant, authority)
	if err != nil {
		return model.Message{}, err
	}
	return message, nil
}

// Overlapping windows may add target-only navigation metadata. Conflicting
// observations cannot describe one deduplicated result and reject the batch.
func mergeContextMessage(a, b model.Message) (model.Message, error) {
	normalize := func(m model.Message) model.Message {
		m.ReplyChain = nil
		m.DiscussionRoot = nil
		if m.Image != nil {
			copy := *m.Image
			copy.Handle = ""
			m.Image = &copy
		}
		if m.Document != nil {
			copy := *m.Document
			copy.Handle = ""
			m.Document = &copy
		}
		if m.Voice != nil {
			copy := *m.Voice
			copy.Handle = ""
			m.Voice = &copy
		}
		return m
	}
	if !reflect.DeepEqual(normalize(a), normalize(b)) {
		return model.Message{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	if a.ReplyChain != nil {
		if b.ReplyChain != nil && !reflect.DeepEqual(a.ReplyChain, b.ReplyChain) {
			return model.Message{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		b.ReplyChain = a.ReplyChain
	}
	if a.DiscussionRoot != nil {
		if b.DiscussionRoot != nil && *a.DiscussionRoot != *b.DiscussionRoot {
			return model.Message{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		b.DiscussionRoot = a.DiscussionRoot
	}
	return b, nil
}
