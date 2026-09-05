package reader

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func searchEnvelope(t *testing.T, result Result, err error) model.Envelope[model.SearchHit] {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[model.SearchHit]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ReadEffect.Kind != model.ReadEffectNone {
		t.Fatal("search changed read state")
	}
	return envelope
}

func TestSearchFiltersSnippetsAndContinuesFromFetchedWindow(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	hidden := candidate(g, 20, "never expose")
	hidden.Forwarded = true
	f.items = []model.Candidate{candidate(g, 25, strings.Repeat("世", 241)), hidden}
	result, err := s.Search(context.Background(), "req_search", g.Peer, "  find me  ", 2, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 1 || !page.Partial || page.NextCursor == nil || !page.Items[0].SnippetTruncated || utf8.RuneCountInString(page.Items[0].Snippet) != 240 || f.ackCalls != 0 {
		t.Fatal("incorrect snippet window")
	}
	if strings.Contains(string(result.JSON), "never expose") || f.searchQuery.Query != "find me" || f.searchQuery.MinID != 10 || f.searchQuery.MaxID != 30 {
		t.Fatal("unsafe search scope")
	}
	token := *page.NextCursor
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil || strings.Contains(string(payload), "find me") {
		t.Fatal("cursor contains query text")
	}
	f.items = []model.Candidate{candidate(g, 19, "older"), candidate(g, 15, "older still")}
	result, err = s.Search(context.Background(), "req_next", g.Peer, "find me", 2, token)
	page = searchEnvelope(t, result, err)
	if f.searchQuery.Before != 20 || f.searchQuery.MaxID != 25 || page.NextCursor == nil {
		t.Fatal("cursor did not anchor/advance using filtered window")
	}
	f.items = []model.Candidate{candidate(g, 10, "last")}
	result, err = s.Search(context.Background(), "req_last", g.Peer, "find me", 2, *page.NextCursor)
	page = searchEnvelope(t, result, err)
	if page.NextCursor != nil || page.Partial || f.ackCalls != 0 {
		t.Fatal("terminal page mismatch")
	}
}

func TestFullyFilteredSearchPageStillAdvances(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	c := candidate(g, 20, "hidden")
	c.Protected = true
	f.items = []model.Candidate{c}
	result, err := s.Search(context.Background(), "req_filter", g.Peer, "q", 1, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 0 || page.NextCursor == nil || !page.Partial {
		t.Fatal("filtered page lost continuation")
	}
	f.items = nil
	result, err = s.Search(context.Background(), "req_empty", g.Peer, "q", 1, *page.NextCursor)
	page = searchEnvelope(t, result, err)
	if page.NextCursor != nil || len(page.Items) != 0 || page.Partial || f.searchQuery.Before != 20 {
		t.Fatal("empty terminal page mismatch")
	}
}

func TestSearchCursorRejectsChangedAuthorityBeforeIO(t *testing.T) {
	for _, change := range []string{"query", "peer", "limit", "tamper", "expired", "regrant", "revoke_regrant", "epoch", "key"} {
		t.Run(change, func(t *testing.T) {
			s, f, p, db, g := testService(t)
			saveGrant(t, p, g)
			f.items = []model.Candidate{candidate(g, 20, "match")}
			result, err := s.Search(context.Background(), "req_start", g.Peer, "q", 1, "")
			page := searchEnvelope(t, result, err)
			token := *page.NextCursor
			query, peer, limit := "q", g.Peer, 1
			switch change {
			case "query":
				query = "different"
			case "peer":
				peer, _ = model.NewPeerID(model.PeerKindChat, 99)
			case "limit":
				limit = 2
			case "tamper":
				token = token[:len(token)-1] + "!"
			case "expired":
				s.now = func() time.Time { return g.ExpiresAt.Add(-30 * time.Minute) }
			case "regrant":
				saveGrant(t, p, g)
			case "revoke_regrant":
				lease, err := p.Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := lease.Revoke(context.Background(), g.Peer); err != nil {
					t.Fatal(err)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
				saveGrant(t, p, g)
			case "epoch":
				if _, err := db.Exec("UPDATE authorization_state SET epoch=?", strings.Repeat("n", 43)); err != nil {
					t.Fatal(err)
				}
				saveGrant(t, p, g)
			case "key":
				s.cursorKey[0]++
			}
			result, err = s.Search(context.Background(), "req_changed", peer, query, limit, token)
			if err == nil || len(result.JSON) != 0 || f.historyCalls != 1 || f.ackCalls != 0 {
				t.Fatal("invalid cursor reached Telegram")
			}
		})
	}
}

func TestSearchDenialAndFailuresReleaseNothing(t *testing.T) {
	for _, failure := range []string{"denied", "upstream", "cancel", "expired", "cursor_expired", "audit", "crosspeer", "bounds", "duplicates"} {
		t.Run(failure, func(t *testing.T) {
			s, f, p, db, g := testService(t)
			if failure != "denied" {
				saveGrant(t, p, g)
			}
			f.items = []model.Candidate{candidate(g, 20, "private")}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "upstream":
				f.historyError = errors.New("private error")
			case "cancel":
				f.onHistory = cancel
			case "expired":
				f.onHistory = func() { s.now = func() time.Time { return g.ExpiresAt } }
			case "cursor_expired":
				expired := s.now().Add(cursorLifetime)
				f.onHistory = func() { s.now = func() time.Time { return expired } }
			case "audit":
				if _, err := db.Exec("DROP TABLE text_audit"); err != nil {
					t.Fatal(err)
				}
			case "crosspeer":
				other, _ := model.NewPeerID(model.PeerKindChat, 99)
				f.items[0].Message.ID, _ = model.NewMessageID(other, 20)
			case "bounds":
				f.items[0] = candidate(g, 31, "private")
			case "duplicates":
				f.items = append(f.items, f.items[0])
			}
			result, err := s.Search(ctx, "req_failure", g.Peer, "q", 20, "")
			if err == nil || len(result.JSON) != 0 || f.ackCalls != 0 {
				t.Fatal("failed search released a result")
			}
			if failure == "denied" && f.historyCalls != 0 {
				t.Fatal("denied search fetched")
			}
		})
	}
}

func TestUnreadOnlyQueriesGrantsAndReturnsMetadata(t *testing.T) {
	s, f, p, _, g := testService(t)
	result, err := s.ListUnread(context.Background(), "req_none")
	if err != nil || f.unreadCalls != 0 || !strings.Contains(string(result.JSON), `"items":[]`) {
		t.Fatal("empty grants caused I/O")
	}
	saveGrant(t, p, g)
	for _, value := range []model.Unread{{Count: 5}, {Marked: true}, {}} {
		f.unread = value
		result, err = s.ListUnread(context.Background(), "req_unread")
		if err != nil {
			t.Fatal(err)
		}
		var page model.Envelope[model.Unread]
		if err := json.Unmarshal(result.JSON, &page); err != nil {
			t.Fatal(err)
		}
		expected := 1
		if value.Count == 0 && !value.Marked {
			expected = 0
		}
		if len(page.Items) != expected || page.ReadEffect.Kind != model.ReadEffectNone || page.NextCursor != nil || f.ackCalls != 0 || f.historyCalls != 0 {
			t.Fatal("unread contract mismatch")
		}
	}
}

func TestUnreadFailsAsWholeResult(t *testing.T) {
	for _, failure := range []string{"upstream", "negative", "expiry", "audit", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			s, f, p, db, g := testService(t)
			saveGrant(t, p, g)
			f.unread.Count = 5
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "upstream":
				f.unreadError = errors.New("private failure")
			case "negative":
				f.unread.Count = -1
			case "expiry":
				f.onUnread = func() { s.now = func() time.Time { return g.ExpiresAt } }
			case "audit":
				if _, err := db.Exec("DROP TABLE text_audit"); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				f.onUnread = cancel
			}
			result, err := s.ListUnread(ctx, "req_failed")
			if err == nil || len(result.JSON) != 0 || f.ackCalls != 0 {
				t.Fatal("failed unread lookup released a result")
			}
		})
	}
}

func TestSearchQueryValidationPrecedesIO(t *testing.T) {
	s, f, _, _, g := testService(t)
	for _, q := range []string{"", " \n ", strings.Repeat("a", 257), strings.Repeat("界", 400), string([]byte{255})} {
		if _, err := s.Search(context.Background(), "req_invalid", g.Peer, q, 20, ""); model.TextErrorCategory(err) != model.ErrorInvalidInput {
			t.Fatal("invalid query accepted")
		}
	}
	if f.historyCalls != 0 {
		t.Fatal("invalid query fetched")
	}
}

func TestSearchBudgetsEscapedWireMirrors(t *testing.T) {
	s, f, p, _, g := testService(t)
	g.MaxID = 109
	saveGrant(t, p, g)
	for id := int32(10); id <= 109; id++ {
		f.items = append(f.items, candidate(g, id, strings.Repeat("<", 240)))
	}
	result, err := s.Search(context.Background(), "req_large", g.Peer, "q", 100, "")
	if model.TextErrorCategory(err) != model.ErrorResultTooLarge || len(result.JSON) != 0 || f.ackCalls != 0 {
		t.Fatal("oversized escaped result released", err)
	}
}
