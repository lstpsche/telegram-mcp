package reader

import (
	"context"
	"reflect"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type searchPage struct {
	items    []model.SearchHit
	partial  bool
	next     *string
	coverage *model.ScopeCoverage
	lookups  int
	check    func() error
	searches []model.SearchAssociation
}

func (s *Service) completeSearch(ctx context.Context, requestID, operation string, fetch func(context.Context, *policy.Lease) (searchPage, error)) (result Result, resultErr error) {
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	lease, err := s.policy.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	var page searchPage
	count := 0
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, operation, count, false, resultErr, func() error { return page.check() })
		if resultErr != nil {
			result = Result{}
		}
	}()
	page, err = fetch(ctx, lease)
	if err != nil {
		return Result{}, err
	}
	if len(page.searches) == 0 {
		result, err = prepare(requestID, page.items, s.now(), page.partial, model.NoReadEffect(), page.next, page.coverage)
	} else {
		state := model.FreshnessLive
		if page.lookups == 0 {
			state = model.FreshnessUnavailable
		}
		freshness, e := model.NewFreshness(state, s.now())
		if e != nil {
			return Result{}, e
		}
		envelope, e := model.NewEnvelope(requestID, freshness, page.items)
		if e != nil {
			return Result{}, e
		}
		envelope.Searches = page.searches
		envelope.Partial = page.partial
		if page.partial {
			envelope.Warnings = append(envelope.Warnings, model.WarningPartialResult)
		}
		result, err = serializeEnvelope(envelope)
	}
	if err != nil {
		return Result{}, err
	}
	count = len(page.items)
	return result, nil
}

// Searches shares authority and delivery bounds across independently paginated searches.
func (s *Service) Searches(ctx context.Context, requestID string, requests []model.SearchRequest) (Result, error) {
	if len(requests) == 0 || len(requests) > model.MaximumSearchRequests {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	requests = append([]model.SearchRequest(nil), requests...)
	total := 0
	for i := range requests {
		r := &requests[i]
		var err error
		r.Filter, err = r.Filter.Normalize()
		if err != nil {
			return Result{}, err
		}
		if (r.Peer.String() == "") == (r.Scope.String() == "") || model.ValidatePageSize(r.Limit) != nil || len(r.Cursor) > 4096 {
			return Result{}, model.TextError(model.ErrorInvalidInput, nil)
		}
		if r.Scope.String() != "" {
			if _, err := model.ParseScopeID(r.Scope.String()); err != nil || r.Filter.HasSavedFilter() || r.Filter.ReplyTo != "" || r.Filter.ThreadRoot != "" {
				return Result{}, model.TextError(model.ErrorInvalidInput, nil)
			}
		} else if r.Filter.CheckReplyPeer(r.Peer) != nil || r.Filter.HasSavedFilter() && r.Peer.Kind() != model.PeerKindSelf {
			return Result{}, model.TextError(model.ErrorInvalidInput, nil)
		}
		total += r.Limit
	}
	if total > model.MaximumPageSize {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	return s.completeSearch(ctx, requestID, "search_messages", func(ctx context.Context, lease *policy.Lease) (searchPage, error) {
		pages := make([]searchPage, 0, len(requests))
		check := func() error {
			for _, p := range pages {
				if err := p.check(); err != nil {
					return err
				}
			}
			return nil
		}
		result := searchPage{items: make([]model.SearchHit, 0), searches: make([]model.SearchAssociation, 0, len(requests)), check: check}
		seen := map[model.MessageID]model.SearchHit{}
		for i, r := range requests {
			if err := check(); err != nil {
				return searchPage{}, err
			}
			var page searchPage
			var err error
			if r.Scope.String() != "" {
				// Reserve one lookup for every remaining search. Scope cursors retain the rest.
				budget := policy.MaximumScopePeers - result.lookups - (len(requests) - i - 1)
				page, err = s.searchScopePage(ctx, lease, r.Scope, r.Filter, r.Limit, r.Cursor, nil, "", budget)
			} else {
				page, err = s.searchPeer(ctx, lease, r.Peer, r.Filter, r.Limit, r.Cursor)
			}
			if err != nil {
				return searchPage{}, err
			}
			pages = append(pages, page)
			association := model.SearchAssociation{Messages: make([]model.MessageID, 0, len(page.items)), Partial: page.partial, NextCursor: page.next, Scope: page.coverage}
			for _, hit := range page.items {
				association.Messages = append(association.Messages, hit.ID)
				if previous, ok := seen[hit.ID]; ok {
					if !reflect.DeepEqual(searchObservation(previous), searchObservation(hit)) {
						return searchPage{}, model.TextError(model.ErrorInvalidReference, nil)
					}
				} else {
					seen[hit.ID] = hit
					result.items = append(result.items, hit)
				}
			}
			result.searches = append(result.searches, association)
			result.partial = result.partial || page.partial
			result.lookups += page.lookups
		}
		return result, nil
	})
}

func searchObservation(hit model.SearchHit) model.SearchHit {
	if hit.Image != nil {
		value := *hit.Image
		value.Handle = ""
		hit.Image = &value
	}
	if hit.Document != nil {
		value := *hit.Document
		value.Handle = ""
		hit.Document = &value
	}
	if hit.Voice != nil {
		value := *hit.Voice
		value.Handle = ""
		hit.Voice = &value
	}
	return hit
}
