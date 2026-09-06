package reader

import (
	"context"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type scopeInfo struct {
	ID            model.ScopeID `json:"id"`
	Name          string        `json:"name"`
	TotalPeers    int           `json:"total_peers"`
	EligiblePeers int           `json:"eligible_peers"`
	ExcludedPeers int           `json:"excluded_peers"`
}

func (s *Service) scopeGrants(scope policy.Scope, grants []policy.Grant) ([]policy.Grant, *model.ScopeCoverage) {
	members := make(map[model.PeerID]bool, len(scope.Peers))
	for _, peer := range scope.Peers {
		members[peer] = true
	}
	selected := make([]policy.Grant, 0, len(scope.Peers))
	// Lease.List returns canonical peer order. Scope membership never adds authority.
	for _, grant := range grants {
		if members[grant.Peer] && (grant.Profile != policy.ProfileSelfAuthored || grant.Author == s.backend.SelfID()) {
			selected = append(selected, grant)
		}
	}
	return selected, &model.ScopeCoverage{ID: scope.ID, TotalPeers: len(scope.Peers), EligiblePeers: len(selected), ExcludedPeers: len(scope.Peers) - len(selected)}
}

func (s *Service) selectGrants(ctx context.Context, lease *policy.Lease, scopes []model.ScopeID) ([]policy.Grant, *model.ScopeCoverage, error) {
	if len(scopes) > 1 {
		return nil, nil, model.TextError(model.ErrorInvalidInput, nil)
	}
	var scope policy.Scope
	if len(scopes) == 1 {
		var err error
		scope, err = lease.Scope(ctx, scopes[0])
		if err != nil {
			return nil, nil, err
		}
	}
	full, err := lease.FullRead(ctx)
	if err != nil {
		return nil, nil, err
	}
	if full {
		if len(scopes) == 0 {
			return nil, nil, model.TextError(model.ErrorInvalidInput, nil)
		}
		grants := make([]policy.Grant, 0, len(scope.Peers))
		for _, peer := range scope.Peers {
			grant, err := lease.Grant(ctx, peer)
			if err != nil {
				return nil, nil, err
			}
			if peer.Kind() == model.PeerKindSelf && peer.TelegramID() != s.backend.SelfID().TelegramID() {
				continue
			}
			grants = append(grants, grant)
		}
		selected, coverage := s.scopeGrants(scope, grants)
		return selected, coverage, nil
	}
	grants, err := lease.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(scopes) == 0 {
		return grants, nil, nil
	}
	selected, coverage := s.scopeGrants(scope, grants)
	return selected, coverage, nil
}

// ListScopes exposes local names and eligibility counts, never ungranted peers.
func (s *Service) ListScopes(ctx context.Context, requestID string) (result Result, resultErr error) {
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
		resultErr = s.finish(ctx, lease, requestID, "list_scopes", count, false, resultErr)
		if resultErr != nil {
			result = Result{}
		}
	}()
	scopes, err := lease.Scopes(ctx)
	if err != nil {
		return Result{}, err
	}
	items := make([]scopeInfo, 0, len(scopes))
	for _, scope := range scopes {
		grants, coverage, err := s.selectGrants(ctx, lease, []model.ScopeID{scope.ID})
		if err != nil {
			return Result{}, err
		}
		if err := s.checkGrantsCurrent(ctx, grants); err != nil {
			return Result{}, err
		}
		items = append(items, scopeInfo{ID: scope.ID, Name: scope.Name, TotalPeers: coverage.TotalPeers, EligiblePeers: coverage.EligiblePeers, ExcludedPeers: coverage.ExcludedPeers})
	}
	freshness, err := model.NewFreshness(model.FreshnessUnavailable, s.now())
	if err != nil {
		return Result{}, err
	}
	envelope, err := model.NewEnvelope(requestID, freshness, items)
	if err != nil {
		return Result{}, err
	}
	result, err = serializeEnvelope(envelope)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}

func (s *Service) checkGrantsCurrent(ctx context.Context, grants []policy.Grant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.Ready() {
		return model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	for _, grant := range grants {
		if err := grant.CheckCurrent(s.now()); err != nil {
			return err
		}
	}
	return nil
}

// SearchScope traverses exact peers in canonical order, newest first per peer.
// The candidate budget includes filtered entries, so sparse pages stay bounded.
func (s *Service) SearchScope(ctx context.Context, requestID string, scopeID model.ScopeID, query string, limit int, token string) (Result, error) {
	query, err := model.NormalizeSearchQuery(query)
	if err != nil {
		return Result{}, err
	}
	return s.searchScope(ctx, requestID, scopeID, query, limit, token, nil)
}

// CatchUp discovers bounded snippets in an explicit window without read receipts.
func (s *Service) CatchUp(ctx context.Context, requestID string, scopeID model.ScopeID, window model.DateWindow, limit int, token string) (Result, error) {
	if err := window.Validate(); err != nil {
		return Result{}, err
	}
	return s.searchScope(ctx, requestID, scopeID, "", limit, token, &window)
}

func (s *Service) searchScope(ctx context.Context, requestID string, scopeID model.ScopeID, query string, limit int, token string, window *model.DateWindow) (result Result, resultErr error) {
	operation := "search_messages"
	if window != nil {
		operation = "catch_up"
	}

	if _, err := model.ParseScopeID(scopeID.String()); err != nil || model.ValidatePageSize(limit) != nil || len(token) > 4096 {
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
	var grants []policy.Grant
	var cursor scopeSearchCursor
	beforeRelease := func() error {
		if err := s.checkGrantsCurrent(releaseContext, grants); err != nil {
			return err
		}
		if cursor.Expires <= s.now().Unix() {
			return model.TextError(model.ErrorCursorExpired, nil)
		}
		return nil
	}
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, operation, count, false, resultErr, beforeRelease)
		if resultErr != nil {
			result = Result{}
		}
	}()
	grants, coverage, err := s.selectGrants(ctx, lease, []model.ScopeID{scopeID})
	if err != nil {
		return Result{}, err
	}
	epoch, revision, err := lease.Binding(ctx)
	if err != nil {
		return Result{}, err
	}
	binding := scopeCursorBinding{Operation: operation, Scope: scopeID, MembersDigest: s.scopeMembersDigest(grants), QueryDigest: s.queryDigest(query), Limit: limit, Epoch: epoch, Revision: revision}
	if window != nil {
		binding.Since, binding.Until = window.Since, window.Until
		coverage.CatchUp = &model.CatchUpCoverage{
			Since: time.Unix(window.Since, 0).UTC().Format(time.RFC3339),
			Until: time.Unix(window.Until, 0).UTC().Format(time.RFC3339),
			Peers: make([]model.CatchUpPeer, len(grants)),
		}
		for i, grant := range grants {
			coverage.CatchUp.Peers[i] = model.CatchUpPeer{Peer: grant.Peer, State: "pending"}
		}
	}
	deadline := s.now().Add(cursorLifetime)
	for _, grant := range grants {
		deadline = grant.Deadline(deadline)
	}
	cursor = scopeSearchCursor{Binding: binding, Expires: deadline.Unix()}
	if len(grants) > 0 {
		cursor.Ceiling = grants[0].MaxID
	}
	if token != "" {
		cursor, err = s.decodeScopeCursor(token, binding)
		if err != nil {
			return Result{}, err
		}
		if cursor.Index >= len(grants) || cursor.Expires > deadline.Unix() {
			return Result{}, model.TextError(model.ErrorCursorInvalid, nil)
		}
		grant := grants[cursor.Index]
		if cursor.Ceiling > grant.MaxID || cursor.Ceiling < grant.MinID || (cursor.Before != 0 && cursor.Before <= grant.MinID) {
			return Result{}, model.TextError(model.ErrorCursorInvalid, nil)
		}
	}
	ctx, stop := context.WithTimeout(ctx, time.Unix(cursor.Expires, 0).Sub(s.now()))
	defer stop()
	items := make([]model.SearchHit, 0)
	remaining := limit
	partial := coverage.ExcludedPeers > 0
	for cursor.Index < len(grants) && remaining > 0 {
		if err := s.checkGrantsCurrent(ctx, grants); err != nil {
			return Result{}, err
		}
		grant := grants[cursor.Index]
		search := model.SearchQuery{Window: window, Peer: grant.Peer, Query: query, MinID: grant.MinID, MaxID: cursor.Ceiling, Before: cursor.Before, Limit: remaining}
		candidates, err := s.backend.Search(ctx, search)
		if err != nil {
			return Result{}, err
		}
		coverage.QueriedPeers++
		normalized, err := s.normalizeSearchWindow(grant, search, candidates, mediaAuthority{epoch, revision})
		if err != nil {
			return Result{}, err
		}
		items = append(items, normalized.items...)
		if coverage.CatchUp != nil {
			peer := &coverage.CatchUp.Peers[cursor.Index]
			peer.Fetched, peer.Returned = len(candidates), len(normalized.items)
		}
		// Filtering makes this page partial independently of whether traversal ended.
		if window != nil {
			partial = partial || normalized.partial
		} else {
			partial = partial || len(normalized.items) < len(candidates)
		}
		remaining -= len(candidates)
		if len(candidates) == search.Limit && normalized.lowest > grant.MinID && !normalized.exhausted {
			if cursor.Before == 0 {
				cursor.Ceiling = normalized.highest
			}
			cursor.Before = normalized.lowest
			break
		}
		cursor.Index++
		cursor.Before = 0
		if cursor.Index < len(grants) {
			cursor.Ceiling = grants[cursor.Index].MaxID
		}
	}
	if err := s.checkGrantsCurrent(ctx, grants); err != nil {
		return Result{}, err
	}
	if cursor.Expires <= s.now().Unix() {
		return Result{}, model.TextError(model.ErrorCursorExpired, nil)
	}
	coverage.CompletedPeers = cursor.Index
	if coverage.CatchUp != nil {
		for i := range coverage.CatchUp.Peers {
			switch {
			case i < cursor.Index:
				coverage.CatchUp.Peers[i].State = "complete"
			case i == cursor.Index && cursor.Before != 0:
				coverage.CatchUp.Peers[i].State = "in_progress"
			}
		}
	}
	var next *string
	if cursor.Index < len(grants) {
		encoded, err := s.encodeScopeCursor(cursor)
		if err != nil {
			return Result{}, err
		}
		next = &encoded
		partial = true
	}
	result, err = prepare(requestID, items, s.now(), partial, model.NoReadEffect(), next, coverage)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}
