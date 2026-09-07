package reader

import (
	"context"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestUnreadMentionsSearchRechecksEvidenceAndPreservesContinuation(t *testing.T) {
	s, f, repository, _, grant := testService(t)
	saveGrant(t, repository, grant)
	filter := model.SearchFilter{UnreadMentionsOnly: true}
	f.items = []model.Candidate{candidate(grant, 20, "ordinary")}
	result, err := s.Search(context.Background(), "req_mentions", grant.Peer, filter, 1, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 0 || !page.Partial || page.NextCursor == nil {
		t.Fatal("ordinary window lost continuation")
	}
	mentioned := candidate(grant, 19, "mentioned")
	mentioned.UnreadMention = true
	f.items = []model.Candidate{mentioned}
	result, err = s.Search(context.Background(), "req_next", grant.Peer, filter, 1, *page.NextCursor)
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 1 || f.searchQuery.Before != 20 || !f.searchQuery.UnreadMentionsOnly || f.searchQuery.Query != "" || f.ackCalls != 0 {
		t.Fatal("mentioned continuation changed filter or receipts")
	}
	mentioned.Protected = true
	f.items = []model.Candidate{mentioned}
	result, err = s.Search(context.Background(), "req_protected", grant.Peer, filter, 1, "")
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 0 || !page.Partial {
		t.Fatal("mention bypassed policy")
	}
}

func TestUnreadMentionsSearchCursorRejectsFilterChangesBeforeIO(t *testing.T) {
	for _, mentionsOnly := range []bool{false, true} {
		s, f, repository, _, grant := testService(t)
		saveGrant(t, repository, grant)
		c := candidate(grant, 20, "match")
		c.UnreadMention = true
		f.items = []model.Candidate{c}
		filter := model.SearchFilter{Query: "q", UnreadMentionsOnly: mentionsOnly}
		result, err := s.Search(context.Background(), "req_first", grant.Peer, filter, 1, "")
		page := searchEnvelope(t, result, err)
		filter.UnreadMentionsOnly = !mentionsOnly
		result, err = s.Search(context.Background(), "req_changed", grant.Peer, filter, 1, *page.NextCursor)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || f.historyCalls != 1 {
			t.Fatal("changed mention filter reached backend", err)
		}
	}
}

func TestUnreadMentionsScopeSearchAdvancesAcrossFilteredPeers(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	f.messages[grants[0].Peer] = []model.Candidate{candidate(grants[0], 20, "ordinary")}
	c := candidate(grants[1], 10, "mention")
	c.UnreadMention = true
	f.messages[grants[1].Peer] = []model.Candidate{c}
	filter := model.SearchFilter{UnreadMentionsOnly: true}
	result, err := s.SearchScope(context.Background(), "req_first", scope.ID, filter, 1, "")
	page := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(page.Items) != 0 || !page.Partial || page.NextCursor == nil {
		t.Fatal("empty mentioned scope window lost continuation")
	}
	result, err = s.SearchScope(context.Background(), "req_next", scope.ID, filter, 1, *page.NextCursor)
	page = decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(page.Items) != 1 || page.NextCursor != nil || page.Scope.CompletedPeers != 2 || f.ackCalls != 0 {
		t.Fatal("mentioned scope traversal failed")
	}
	for _, q := range f.queries {
		if !q.UnreadMentionsOnly || q.Query != "" || q.Window != nil {
			t.Fatal("scope dropped mention filter")
		}
	}
}

func TestUnreadMentionsScopeCursorRejectsFilterChangesBeforeIO(t *testing.T) {
	for _, mentionsOnly := range []bool{false, true} {
		s, f, _, _, grants, scope := scopeService(t)
		c := candidate(grants[0], 20, "mention")
		c.UnreadMention = true
		f.messages[grants[0].Peer] = []model.Candidate{c}
		filter := model.SearchFilter{Query: "q", UnreadMentionsOnly: mentionsOnly}
		result, err := s.SearchScope(context.Background(), "req_first", scope.ID, filter, 1, "")
		page := decodeScopeEnvelope[model.SearchHit](t, result, err)
		calls := len(f.queries)
		filter.UnreadMentionsOnly = !mentionsOnly
		result, err = s.SearchScope(context.Background(), "req_changed", scope.ID, filter, 1, *page.NextCursor)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || len(f.queries) != calls {
			t.Fatal("scope filter change reached backend", err)
		}
	}
}

func TestUnreadMentionsIntersectTextAndPins(t *testing.T) {
	s, f, repo, _, grant := testService(t)
	saveGrant(t, repo, grant)
	c := candidate(grant, 20, "Hello WORLD")
	c.UnreadMention = true
	c.Message.Pinned = true
	f.items = []model.Candidate{c}
	for _, query := range []string{"world", "missing"} {
		result, err := s.Search(context.Background(), "req_mentions", grant.Peer, model.SearchFilter{UnreadMentionsOnly: true, PinnedOnly: true, Query: query}, 2, "")
		page := searchEnvelope(t, result, err)
		expected := 0
		if query == "world" {
			expected = 1
		}
		if len(page.Items) != expected || f.ackCalls != 0 {
			t.Fatal("mention intersection failed")
		}
	}
}
