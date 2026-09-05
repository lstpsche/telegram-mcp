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
	grants, err := lease.List(ctx)
	if err != nil {
		return Result{}, err
	}
	items := make([]scopeInfo, 0, len(scopes))
	for _, scope := range scopes {
		_, coverage := s.scopeGrants(scope, grants)
		items = append(items, scopeInfo{ID: scope.ID, Name: scope.Name, TotalPeers: coverage.TotalPeers, EligiblePeers: coverage.EligiblePeers, ExcludedPeers: coverage.ExcludedPeers})
	}
	if err := s.checkGrantsCurrent(ctx, grants); err != nil {
		return Result{}, err
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
func (s *Service) SearchScope(ctx context.Context, requestID string, scopeID model.ScopeID, query string, limit int, token string) (result Result, resultErr error) {
	query, err := model.NormalizeSearchQuery(query)
	if err != nil {
		return Result{}, err
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
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, "search_messages", count, false, resultErr)
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
	binding := scopeCursorBinding{Operation: "search_messages", Scope: scopeID, MembersDigest: s.scopeMembersDigest(grants), QueryDigest: s.queryDigest(query), Limit: limit, Epoch: epoch, Revision: revision}
	deadline := s.now().Add(cursorLifetime)
	for _, grant := range grants {
		if grant.ExpiresAt.Before(deadline) {
			deadline = grant.ExpiresAt
		}
	}
	cursor := scopeSearchCursor{Binding: binding, Expires: deadline.Unix()}
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
		search := model.SearchQuery{Peer: grant.Peer, Query: query, MinID: grant.MinID, MaxID: cursor.Ceiling, Before: cursor.Before, Limit: remaining}
		candidates, err := s.backend.Search(ctx, search)
		if err != nil {
			return Result{}, err
		}
		coverage.QueriedPeers++
		window, err := s.normalizeSearchWindow(grant, search, candidates)
		if err != nil {
			return Result{}, err
		}
		items = append(items, window.items...)
		// Filtering makes this page partial independently of whether traversal ended.
		partial = partial || len(window.items) < len(candidates)
		remaining -= len(candidates)
		if len(candidates) == search.Limit && window.lowest > grant.MinID {
			if cursor.Before == 0 {
				cursor.Ceiling = window.highest
			}
			cursor.Before = window.lowest
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
