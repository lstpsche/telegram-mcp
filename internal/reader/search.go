package reader

import (
	"context"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func (s *Service) Search(ctx context.Context, requestID string, peer model.PeerID, filter model.SearchFilter, limit int, token string) (result Result, resultErr error) {
	filter, err := filter.Normalize()
	if err != nil {
		return Result{}, err
	}
	if filter.CheckReplyPeer(peer) != nil || (filter.HasSavedFilter() && peer.Kind() != model.PeerKindSelf) || peer.String() == "" || model.ValidatePageSize(limit) != nil || len(token) > 4096 {
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
	releaseContext := ctx
	var grant policy.Grant
	var cursor searchCursor
	beforeRelease := func() error {
		if err := s.checkGrantsCurrent(releaseContext, []policy.Grant{grant}); err != nil {
			return err
		}
		if cursor.Expires <= s.now().Unix() {
			return model.TextError(model.ErrorCursorExpired, nil)
		}
		return nil
	}
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, "search_messages", count, false, resultErr, beforeRelease)
		if resultErr != nil {
			result = Result{}
		}
	}()
	grant, err = lease.Grant(ctx, peer)
	if err != nil {
		return Result{}, err
	}
	if grant.Profile == policy.ProfileSelfAuthored && grant.Author != s.backend.SelfID() {
		return Result{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	ctx, expires := context.WithTimeout(ctx, grant.Deadline(s.now().Add(OperationTimeout)).Sub(s.now()))
	defer expires()
	epoch, revision, err := lease.Binding(ctx)
	if err != nil {
		return Result{}, err
	}
	binding := cursorBinding{ReplyTo: filter.ReplyTo, ThreadRoot: filter.ThreadRoot, SavedPeer: filter.SavedPeer, SavedTagDigest: s.savedTagDigest(filter.SavedTag), Sender: filter.Sender, Since: filter.Since, Until: filter.Until, MediaType: filter.MediaType, PinnedOnly: filter.PinnedOnly, Operation: "search_messages", Peer: peer, QueryDigest: s.queryDigest(filter.Query), Limit: limit, Epoch: epoch, Revision: revision}
	deadline := s.now().Add(cursorLifetime)
	deadline = grant.Deadline(deadline)
	cursor = searchCursor{Binding: binding, Ceiling: grant.MaxID, Expires: deadline.Unix()}
	if token != "" {
		cursor, err = s.decodeCursor(token, binding)
		if err != nil {
			return Result{}, err
		}
		if cursor.Before <= grant.MinID || cursor.Ceiling > grant.MaxID || cursor.Expires > grant.Deadline(s.now().Add(cursorLifetime)).Unix() {
			return Result{}, model.TextError(model.ErrorCursorInvalid, nil)
		}
	}
	ctx, stopCursor := context.WithTimeout(ctx, time.Unix(cursor.Expires, 0).Sub(s.now()))
	defer stopCursor()
	query := model.SearchQuery{ReplyTo: filter.ReplyTo, ThreadRoot: filter.ThreadRoot, SavedPeer: filter.SavedPeer, SavedTag: filter.SavedTag, Sender: filter.Sender, Since: filter.Since, Until: filter.Until, MediaType: filter.MediaType, PinnedOnly: filter.PinnedOnly, Peer: peer, Query: filter.Query, MinID: grant.MinID, MaxID: cursor.Ceiling, Before: cursor.Before, Limit: limit}
	candidates, err := s.backend.Search(ctx, query)
	if err != nil {
		return Result{}, err
	}
	window, err := s.normalizeSearchWindow(grant, query, candidates, mediaAuthority{epoch, revision})
	if err != nil {
		return Result{}, err
	}
	items, lowest, highest, partial := window.items, window.lowest, window.highest, window.partial
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
	var next *string
	if len(candidates) == limit && lowest > grant.MinID && !window.exhausted {
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
	result, err = prepare(requestID, items, s.now(), partial, model.NoReadEffect(), next, nil)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}

type searchWindow struct {
	items           []model.SearchHit
	lowest, highest int32
	partial         bool
	exhausted       bool
}

// normalizeSearchWindow applies the same bounds and content policy to both selectors.
func (s *Service) normalizeSearchWindow(grant policy.Grant, query model.SearchQuery, candidates []model.Candidate, authority mediaAuthority) (searchWindow, error) {
	if len(candidates) > query.Limit {
		return searchWindow{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	items := make([]model.SearchHit, 0, len(candidates))
	seen := make(map[int32]bool, len(candidates))
	partial := query.Window == nil && len(candidates) == query.Limit
	exhausted := false
	var lowest, highest int32
	for _, candidate := range candidates {
		message := candidate.Message
		id := message.ID.TelegramID()
		if message.ID.Peer() != query.Peer || id < grant.MinID || id > query.MaxID || (query.Before > 0 && id >= query.Before) || seen[id] {
			return searchWindow{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		seen[id] = true
		if lowest == 0 || id < lowest {
			lowest = id
		}
		if id > highest {
			highest = id
		}
		if query.UsesHistory() && candidate.SentAt != 0 {
			if candidate.SentAt < 0 {
				return searchWindow{}, model.TextError(model.ErrorInvalidReference, nil)
			}
			since, until := query.Since, query.Until
			if query.Window != nil {
				since, until = query.Window.Since, query.Window.Until
			}
			if since != 0 && candidate.SentAt < since {
				exhausted = true
				continue
			}
			if until != 0 && candidate.SentAt >= until {
				continue
			}
		}
		if err := grant.CheckMessage(candidate, s.backend.SelfID(), s.now()); err != nil {
			switch model.TextErrorCategory(err) {
			case model.ErrorPolicyDenied, model.ErrorProtectedContent, model.ErrorEphemeralContent, model.ErrorUnsupportedPeer:
				partial = true
				continue
			default:
				return searchWindow{}, err
			}
		}
		if (query.ReplyTo != "" && (message.ReplyTo == nil || message.ReplyTo.String() != query.ReplyTo)) || (query.ThreadRoot != "" && (message.ThreadRoot == nil || message.ThreadRoot.String() != query.ThreadRoot)) {
			partial = true
			continue
		}
		if query.PinnedOnly && !message.Pinned {
			partial = true
			continue
		}
		if query.Window != nil && (!query.Window.Contains(message.Date) || candidate.SentAt == 0) {
			return searchWindow{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		date, err := time.Parse(time.RFC3339Nano, message.Date)
		if err != nil || date.IsZero() || (query.Window != nil && date.Unix() != candidate.SentAt) || !message.ValidAuthor() || (message.Text == "" && candidate.Image == nil && candidate.Document == nil && candidate.Voice == nil && message.Poll == nil && message.LinkPreview == nil) || !utf8.ValidString(message.Text) {
			return searchWindow{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if query.Since != 0 && date.Unix() < query.Since {
			continue
		}
		if query.Until != 0 && date.Unix() >= query.Until {
			continue
		}
		if (query.SavedPeer != "" && message.SavedPeer != query.SavedPeer) || (query.SavedTag != (model.SavedTag{}) && !query.SavedTag.Matches(message.Reactions)) {
			partial = true
			continue
		}
		if query.Sender != "" && message.Author.String() != query.Sender {
			partial = true
			continue
		}
		if len(message.Text) > 64*1024 {
			return searchWindow{}, model.TextError(model.ErrorResultTooLarge, nil)
		}
		snippetText := message.Text
		if message.Poll != nil {
			snippetText = message.Poll.Question
		}
		snippet := []rune(snippetText)
		truncated := len(snippet) > 240
		if truncated {
			snippet = snippet[:240]
		}
		descriptor, err := s.imageDescriptor(candidate, grant, authority)
		if err != nil {
			return searchWindow{}, err
		}
		document, err := s.documentDescriptor(candidate, grant, authority)
		if err != nil {
			return searchWindow{}, err
		}
		voice, err := s.voiceDescriptor(candidate, grant, authority)
		if err != nil {
			return searchWindow{}, err
		}
		hit := model.SearchHit{DiscussionPeer: message.DiscussionPeer, ThreadRoot: message.ThreadRoot, SavedPeer: message.SavedPeer, Pinned: message.Pinned, Reactions: message.Reactions, HasLinkPreview: message.LinkPreview != nil, HasPoll: message.Poll != nil, AlbumID: message.AlbumID, ReplyTo: message.ReplyTo, ChannelPost: message.ChannelPost, Forward: message.Forward, Voice: voice, Image: descriptor, Document: document, ID: message.ID, Author: message.Author, Date: date.UTC().Format(time.RFC3339Nano), Snippet: string(snippet), SnippetTruncated: truncated}
		if !matchesSearchMedia(query.MediaType, hit) {
			partial = true
			continue
		}
		items = append(items, hit)
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ID.TelegramID() > items[j].ID.TelegramID() })
	return searchWindow{items: items, lowest: lowest, highest: highest, partial: partial, exhausted: exhausted}, nil
}

// Match only descriptors that have passed policy and media validation.
func matchesSearchMedia(kind model.SearchMediaType, hit model.SearchHit) bool {
	switch kind {
	case "":
		return true
	case model.SearchMediaPhoto:
		return hit.Image != nil && hit.Image.Kind == "photo"
	case model.SearchMediaImageFile:
		return hit.Image != nil && hit.Image.Kind == "document"
	case model.SearchMediaPDF:
		return hit.Document != nil && hit.Document.MIMEType == "application/pdf"
	case model.SearchMediaTextFile:
		return hit.Document != nil && hit.Document.MIMEType == "text/plain"
	case model.SearchMediaVoiceNote:
		return hit.Voice != nil
	default:
		return false
	}
}

// ListUnread returns the first page of authorized whole-dialog counts.
// It never substitutes a partial result when any required peer lookup fails.
func (s *Service) ListUnread(ctx context.Context, requestID string, scopes ...model.ScopeID) (Result, error) {
	return s.UnreadPage(ctx, requestID, scopes, "")
}

func (s *Service) UnreadPage(ctx context.Context, requestID string, scopes []model.ScopeID, token string) (result Result, resultErr error) {
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
		resultErr = s.finish(ctx, lease, requestID, "list_unread", count, false, resultErr, func() error {
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
		result, count, err = s.fullDialogs(ctx, lease, requestID, "list_unread", model.MaximumPageSize, token, &dialogExpiry)
		return result, err
	}
	if token != "" {
		return Result{}, model.TextError(model.ErrorCursorInvalid, nil)
	}
	grants, coverage, err := s.selectGrants(ctx, lease, scopes)
	if err != nil {
		return Result{}, err
	}
	items := make([]model.Unread, 0, len(grants))
	for _, grant := range grants {
		if err := s.checkGrantsCurrent(ctx, grants); err != nil {
			return Result{}, err
		}
		bounded, stop := context.WithTimeout(ctx, grant.Deadline(s.now().Add(OperationTimeout)).Sub(s.now()))
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
	if err := s.checkGrantsCurrent(ctx, grants); err != nil {
		return Result{}, err
	}
	if coverage != nil {
		coverage.QueriedPeers = len(grants)
		coverage.CompletedPeers = len(grants)
	}
	result, err = prepare(requestID, items, s.now(), coverage != nil && coverage.ExcludedPeers > 0, model.NoReadEffect(), nil, coverage)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}
