package reader

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestBatchSearchDeduplicatesWithIndependentContinuation(t *testing.T) {
	s, f, _, db, grants, scope := scopeService(t)
	for _, g := range grants {
		f.messages[g.Peer] = []model.Candidate{candidate(g, 25, "match"), candidate(g, 20, "older")}
	}
	requests := []model.SearchRequest{{Peer: grants[0].Peer, Filter: model.SearchFilter{Query: "first"}, Limit: 1}, {Scope: scope.ID, Filter: model.SearchFilter{Query: "second"}, Limit: 1}}
	result, err := s.Searches(context.Background(), "req_batch", requests)
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 1 || len(page.Searches) != 2 || page.NextCursor != nil || !page.Partial || f.ackCalls != 0 {
		t.Fatal("incorrect shared result")
	}
	for i, association := range page.Searches {
		if len(association.Messages) != 1 || association.Messages[0] != page.Items[0].ID || association.NextCursor == nil {
			t.Fatal("lost search association")
		}
		requests[i].Cursor = *association.NextCursor
	}
	if page.Searches[1].Scope == nil {
		t.Fatal("scope coverage lost")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM text_audit").Scan(&count); err != nil || count != 1 {
		t.Fatal("batch must audit once", err)
	}
	result, err = s.Searches(context.Background(), "req_batch_next", requests)
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 1 || page.Items[0].ID.TelegramID() != 20 {
		t.Fatal("continuation failed")
	}
}

func TestBatchSearchRejectsInvalidRequestsBeforeIO(t *testing.T) {
	for _, variant := range []string{"empty", "count", "budget", "selector", "filter"} {
		t.Run(variant, func(t *testing.T) {
			s, f, _, _, g, _ := scopeService(t)
			r := model.SearchRequest{Peer: g[0].Peer, Filter: model.SearchFilter{Query: "q"}, Limit: 20}
			requests := []model.SearchRequest{r, r}
			switch variant {
			case "empty":
				requests = nil
			case "count":
				requests = make([]model.SearchRequest, 11)
			case "budget":
				requests[1].Limit = 100
			case "selector":
				requests[1].Peer = model.PeerID{}
			case "filter":
				requests[1].Filter = model.SearchFilter{}
			}
			result, err := s.Searches(context.Background(), "req_invalid_batch", requests)
			if model.TextErrorCategory(err) != model.ErrorInvalidInput || len(result.JSON) != 0 || len(f.queries) != 0 {
				t.Fatal("invalid batch reached provider", err)
			}
		})
	}
}

func TestBatchSearchWithholdsEarlierHitsOnLaterFailure(t *testing.T) {
	for _, variant := range []string{"provider", "conflict", "expiry", "cancel", "audit"} {
		t.Run(variant, func(t *testing.T) {
			s, f, _, db, grants, _ := scopeService(t)
			g := grants[0]
			f.messages[g.Peer] = []model.Candidate{candidate(g, 25, "first observation")}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.onSearch = func(_ context.Context, _ model.SearchQuery) error {
				if len(f.queries) != 2 {
					return nil
				}
				switch variant {
				case "provider":
					return errors.New("synthetic provider failure")
				case "conflict":
					f.messages[g.Peer][0].Message.Text = "changed observation"
				case "expiry":
					now := s.now()
					s.now = func() time.Time { return now.Add(2 * time.Hour) }
				case "cancel":
					cancel()
				case "audit":
					_, err := db.Exec("DROP TABLE text_audit")
					return err
				}
				return nil
			}
			r := model.SearchRequest{Peer: g.Peer, Filter: model.SearchFilter{Query: "q"}, Limit: 20}
			result, err := s.Searches(ctx, "req_failure_batch", []model.SearchRequest{r, r})
			if err == nil || len(result.JSON) != 0 || f.ackCalls != 0 {
				t.Fatal("failed batch released hits", err)
			}
		})
	}
}

func TestBatchSearchScopeReservesLookupsAndResumesAtPeerBoundary(t *testing.T) {
	s, f, p, _, grants, _ := scopeService(t)
	peers := []model.PeerID{grants[0].Peer, grants[1].Peer}
	for i := 0; i < 18; i++ {
		g := grants[0]
		g.Peer, _ = model.NewPeerID(model.PeerKindChat, int64(1000+i))
		saveGrant(t, p, g)
		peers = append(peers, g.Peer)
	}
	scope := saveScope(t, p, "", "batch-budget", peers...)
	r := model.SearchRequest{Scope: scope.ID, Filter: model.SearchFilter{Query: "q"}, Limit: 20}
	result, err := s.Searches(context.Background(), "req_lookup_budget", []model.SearchRequest{r, r})
	page := searchEnvelope(t, result, err)
	if len(f.queries) != 20 || page.Searches[0].Scope.QueriedPeers != 19 || page.Searches[1].Scope.QueriedPeers != 1 || page.Searches[0].NextCursor == nil {
		t.Fatal("shared lookup budget not enforced")
	}
	result, err = s.SearchScope(context.Background(), "req_resume_scope", scope.ID, r.Filter, r.Limit, *page.Searches[0].NextCursor)
	resumed := searchEnvelope(t, result, err)
	if resumed.NextCursor != nil || resumed.Scope.QueriedPeers != 1 || resumed.Scope.CompletedPeers != 20 {
		t.Fatal("boundary continuation failed")
	}
}

func TestBatchSearchSharesDeadline(t *testing.T) {
	s, f, _, _, grants, _ := scopeService(t)
	var deadline time.Time
	f.onSearch = func(ctx context.Context, _ model.SearchQuery) error {
		value, ok := ctx.Deadline()
		if !ok {
			t.Fatal("search has no deadline")
		}
		if deadline.IsZero() {
			deadline = value
		} else if !deadline.Equal(value) {
			t.Fatal("search reset the batch deadline")
		}
		return nil
	}
	r := model.SearchRequest{Peer: grants[0].Peer, Filter: model.SearchFilter{Query: "q"}, Limit: 20}
	result, err := s.Searches(context.Background(), "req_deadline", []model.SearchRequest{r, r})
	searchEnvelope(t, result, err)
}

func TestBatchSearchBudgetsCombinedEscapedMirrors(t *testing.T) {
	s, f, p, _, grants, _ := scopeService(t)
	requests := make([]model.SearchRequest, 0, 2)
	for _, g := range grants {
		g.MaxID = 59
		saveGrant(t, p, g)
		for id := int32(10); id <= 59; id++ {
			f.messages[g.Peer] = append(f.messages[g.Peer], candidate(g, id, strings.Repeat("<", 240)))
		}
		requests = append(requests, model.SearchRequest{Peer: g.Peer, Filter: model.SearchFilter{Query: "q"}, Limit: 50})
	}
	result, err := s.Searches(context.Background(), "req_batch_large", requests)
	if model.TextErrorCategory(err) != model.ErrorResultTooLarge || len(result.JSON) != 0 || f.ackCalls != 0 {
		t.Fatal("combined oversized batch released", err)
	}
}
