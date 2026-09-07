package reader

import (
	"context"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestSenderDateSearchNarrowsAuthorizedCandidates(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 25, "until"), candidate(g, 24, "match"), candidate(g, 23, "since"), candidate(g, 22, "older")}
	for i := range f.items {
		f.items[i].SentAt = 104 - int64(i)
		f.items[i].Message.Date = time.Unix(104-int64(i), 0).UTC().Format(time.RFC3339)
	}
	filter := model.SearchFilter{Sender: g.Author.String(), Since: 102, Until: 104}
	result, err := s.Search(context.Background(), "req_dates", g.Peer, filter, 10, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 2 || page.Items[0].Snippet != "match" || page.Items[1].Snippet != "since" || f.ackCalls != 0 || f.searchQuery.Sender != filter.Sender || f.searchQuery.Since != 102 || f.searchQuery.Until != 104 {
		t.Fatal("incorrect sender/date intersection", err)
	}
	filter.Sender = "tgpeer:v1:user:999"
	filter.Since, filter.Until = 0, 0
	result, err = s.Search(context.Background(), "req_sender", g.Peer, filter, 4, "")
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 0 || page.NextCursor == nil {
		t.Fatal("sparse page lost continuation")
	}
}

func TestSearchCursorBindsSenderAndDates(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		for _, change := range []func(*model.SearchFilter){func(f *model.SearchFilter) { f.Sender = "tgpeer:v1:user:99" }, func(f *model.SearchFilter) { f.Since = 1 }, func(f *model.SearchFilter) { f.Until = 2000000000 }} {
			s, f, _, _, grants, scope := scopeService(t)
			g := grants[0]
			f.messages[g.Peer] = []model.Candidate{candidate(g, 25, "match")}
			filter := model.SearchFilter{Query: "q"}
			search := func(token string) (Result, error) {
				if scoped {
					return s.SearchScope(context.Background(), "req_cursor", scope.ID, filter, 1, token)
				}
				return s.Search(context.Background(), "req_cursor", g.Peer, filter, 1, token)
			}
			result, err := search("")
			page := searchEnvelope(t, result, err)
			if page.NextCursor == nil {
				t.Fatal("missing fixture cursor")
			}
			calls := len(f.queries)
			change(&filter)
			result, err = search(*page.NextCursor)
			if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || len(f.queries) != calls {
				t.Fatal("changed filter used cursor", err)
			}
		}
	}
}

func TestDateHistoryStopsAfterExcludedOlderCandidate(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	c := candidate(g, 25, "")
	c.Unsupported = true
	c.SentAt = 99
	f.items = []model.Candidate{c}
	result, err := s.Search(context.Background(), "req_end", g.Peer, model.SearchFilter{Since: 100}, 1, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatal("date traversal failed to stop")
	}
}
