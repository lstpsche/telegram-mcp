package reader

import (
	"context"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestPinnedSearchRechecksPinsAndPreservesContinuation(t *testing.T) {
	s, f, repository, _, grant := testService(t)
	saveGrant(t, repository, grant)
	filter := model.SearchFilter{PinnedOnly: true}
	f.items = []model.Candidate{candidate(grant, 20, "unpinned")}
	result, err := s.Search(context.Background(), "req_pins", grant.Peer, filter, 1, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 0 || !page.Partial || page.NextCursor == nil {
		t.Fatal("unpinned window lost continuation")
	}
	pinned := candidate(grant, 19, "pinned")
	pinned.Message.Pinned = true
	f.items = []model.Candidate{pinned}
	result, err = s.Search(context.Background(), "req_next", grant.Peer, filter, 1, *page.NextCursor)
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 1 || !page.Items[0].Pinned || f.searchQuery.Before != 20 || !f.searchQuery.PinnedOnly || f.searchQuery.Query != "" || f.ackCalls != 0 {
		t.Fatal("pinned continuation changed filter or receipts")
	}
	pinned.Protected = true
	f.items = []model.Candidate{pinned}
	result, err = s.Search(context.Background(), "req_protected", grant.Peer, filter, 1, "")
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 0 || !page.Partial {
		t.Fatal("pin bypassed policy")
	}
}

func TestPinnedSearchCursorRejectsFilterChangesBeforeIO(t *testing.T) {
	for _, pinnedOnly := range []bool{false, true} {
		s, f, repository, _, grant := testService(t)
		saveGrant(t, repository, grant)
		c := candidate(grant, 20, "match")
		c.Message.Pinned = true
		f.items = []model.Candidate{c}
		filter := model.SearchFilter{Query: "q", PinnedOnly: pinnedOnly}
		result, err := s.Search(context.Background(), "req_first", grant.Peer, filter, 1, "")
		page := searchEnvelope(t, result, err)
		filter.PinnedOnly = !pinnedOnly
		result, err = s.Search(context.Background(), "req_changed", grant.Peer, filter, 1, *page.NextCursor)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || f.historyCalls != 1 {
			t.Fatal("changed pin filter reached backend", err)
		}
	}
}

func TestPinnedScopeSearchAdvancesAcrossFilteredPeers(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	f.messages[grants[0].Peer] = []model.Candidate{candidate(grants[0], 20, "unpinned")}
	c := candidate(grants[1], 10, "pin")
	c.Message.Pinned = true
	f.messages[grants[1].Peer] = []model.Candidate{c}
	filter := model.SearchFilter{PinnedOnly: true}
	result, err := s.SearchScope(context.Background(), "req_first", scope.ID, filter, 1, "")
	page := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(page.Items) != 0 || !page.Partial || page.NextCursor == nil {
		t.Fatal("empty pinned scope window lost continuation")
	}
	result, err = s.SearchScope(context.Background(), "req_next", scope.ID, filter, 1, *page.NextCursor)
	page = decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(page.Items) != 1 || !page.Items[0].Pinned || page.NextCursor != nil || page.Scope.CompletedPeers != 2 || f.ackCalls != 0 {
		t.Fatal("pinned scope traversal failed")
	}
	for _, q := range f.queries {
		if !q.PinnedOnly || q.Query != "" || q.Window != nil {
			t.Fatal("scope dropped pin filter")
		}
	}
}

func TestPinnedScopeCursorRejectsFilterChangesBeforeIO(t *testing.T) {
	for _, pinnedOnly := range []bool{false, true} {
		s, f, _, _, grants, scope := scopeService(t)
		c := candidate(grants[0], 20, "pin")
		c.Message.Pinned = true
		f.messages[grants[0].Peer] = []model.Candidate{c}
		filter := model.SearchFilter{Query: "q", PinnedOnly: pinnedOnly}
		result, err := s.SearchScope(context.Background(), "req_first", scope.ID, filter, 1, "")
		page := decodeScopeEnvelope[model.SearchHit](t, result, err)
		calls := len(f.queries)
		filter.PinnedOnly = !pinnedOnly
		result, err = s.SearchScope(context.Background(), "req_changed", scope.ID, filter, 1, *page.NextCursor)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || len(f.queries) != calls {
			t.Fatal("scope filter change reached backend", err)
		}
	}
}
