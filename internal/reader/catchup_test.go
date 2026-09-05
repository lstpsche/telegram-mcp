package reader

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func catchUpWindow(t *testing.T) model.DateWindow {
	t.Helper()
	w, err := model.ParseDateWindow("2026-09-05T12:00:00Z", "2026-09-05T12:00:02Z")
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCatchUpCoverageContinuesFilteredPagesAfterReconstruction(t *testing.T) {
	s, f, repository, db, grants, scope := scopeService(t)
	hidden := candidate(grants[0], 20, "excluded synthetic body")
	hidden.Protected = true
	f.messages[grants[0].Peer] = []model.Candidate{hidden}
	f.messages[grants[1].Peer] = []model.Candidate{candidate(grants[1], 10, "visible")}
	w := catchUpWindow(t)
	result, err := s.CatchUp(context.Background(), "req_start", scope.ID, w, 1, "")
	first := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(first.Items) != 0 || first.NextCursor == nil || !first.Partial {
		t.Fatal("filtered page lost continuation")
	}
	peers := first.Scope.CatchUp.Peers
	if peers[0].State != "in_progress" || peers[0].Fetched != 1 || peers[0].Returned != 0 || peers[1].State != "pending" {
		t.Fatalf("wrong progress: %+v", peers)
	}
	if strings.Contains(string(result.JSON), hidden.Message.Text) {
		t.Fatal("excluded body leaked")
	}
	restarted, err := New(f, repository, s.now, s.cursorKey[:])
	if err != nil {
		t.Fatal(err)
	}
	result, err = restarted.CatchUp(context.Background(), "req_end", scope.ID, w, 1, *first.NextCursor)
	last := decodeScopeEnvelope[model.SearchHit](t, result, err)
	peers = last.Scope.CatchUp.Peers
	if len(last.Items) != 1 || last.NextCursor != nil || last.Scope.CompletedPeers != 2 || peers[0].State != "complete" || peers[0].Fetched != 0 || peers[1].State != "complete" || peers[1].Returned != 1 || f.ackCalls != 0 {
		t.Fatalf("wrong terminal coverage: %+v", last)
	}
	for _, q := range f.queries {
		if q.Query != "" || q.Window == nil || *q.Window != w {
			t.Fatal("date bounds lost")
		}
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM text_audit WHERE operation='catch_up' AND outcome='success'").Scan(&count); err != nil || count != 2 {
		t.Fatal("catch-up audit missing", err)
	}
}

func TestCatchUpCursorBindingsRejectBeforeFetch(t *testing.T) {
	for _, change := range []string{"since", "until", "limit", "operation", "scope", "policy", "expired", "key"} {
		t.Run(change, func(t *testing.T) {
			s, f, repository, _, grants, scope := scopeService(t)
			f.messages[grants[0].Peer] = []model.Candidate{candidate(grants[0], 20, "visible")}
			w := catchUpWindow(t)
			result, err := s.CatchUp(context.Background(), "req_first", scope.ID, w, 1, "")
			first := decodeScopeEnvelope[model.SearchHit](t, result, err)
			if first.NextCursor == nil {
				t.Fatal("missing continuation")
			}
			limit := 1
			switch change {
			case "since":
				w.Since--
			case "until":
				w.Until++
			case "limit":
				limit = 2
			case "scope":
				scope = saveScope(t, repository, "", "another", grants[0].Peer)
			case "policy":
				saveGrant(t, repository, grants[0])
			case "expired":
				now := s.now().Add(16 * time.Minute)
				s.now = func() time.Time { return now }
			case "key":
				s.cursorKey[0]++
			}
			if change == "operation" {
				result, err = s.SearchScope(context.Background(), "req_next", scope.ID, "query", limit, *first.NextCursor)
			} else {
				result, err = s.CatchUp(context.Background(), "req_next", scope.ID, w, limit, *first.NextCursor)
			}
			if err == nil || len(result.JSON) != 0 || len(f.queries) != 1 {
				t.Fatal("changed binding fetched or returned content")
			}
		})
	}
}

func TestCatchUpRejectsOutOfWindowProviderResults(t *testing.T) {
	for _, date := range []string{"2026-09-05T11:59:59Z", "2026-09-05T12:00:02Z", "invalid"} {
		s, f, _, _, grants, scope := scopeService(t)
		item := candidate(grants[0], 20, "synthetic")
		item.Message.Date = date
		f.messages[grants[0].Peer] = []model.Candidate{item}
		result, err := s.CatchUp(context.Background(), "req_bad_date", scope.ID, catchUpWindow(t), 20, "")
		if model.TextErrorCategory(err) != model.ErrorInvalidReference || len(result.JSON) != 0 || f.ackCalls != 0 {
			t.Fatal("out-of-window content released")
		}
	}
}

func TestCatchUpLateFailureDiscardsEarlierPeers(t *testing.T) {
	for _, failure := range []string{"upstream", "expiry", "readiness", "audit"} {
		t.Run(failure, func(t *testing.T) {
			s, f, _, db, grants, scope := scopeService(t)
			f.messages[grants[0].Peer] = []model.Candidate{candidate(grants[0], 20, "visible")}
			cause := errors.New("synthetic upstream failure")
			f.onSearch = func(_ context.Context, q model.SearchQuery) error {
				if q.Peer != grants[1].Peer {
					return nil
				}
				switch failure {
				case "upstream":
					return cause
				case "expiry":
					s.now = func() time.Time { return grants[0].ExpiresAt }
				case "readiness":
					f.notReady = true
				case "audit":
					if _, err := db.Exec("DROP TABLE text_audit"); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			result, err := s.CatchUp(context.Background(), "req_failed", scope.ID, catchUpWindow(t), 20, "")
			if err == nil || len(result.JSON) != 0 || f.ackCalls != 0 {
				t.Fatal("failed catch-up returned partial content")
			}
			if failure == "upstream" && !errors.Is(err, cause) {
				t.Fatal("cause lost")
			}
		})
	}
}

func TestCatchUpExcludedPeersAreNotExposedOrClaimedEmpty(t *testing.T) {
	s, f, repository, _, _, scope := scopeService(t)
	unknown, _ := model.NewPeerID(model.PeerKindChat, 99)
	saveScope(t, repository, scope.ID, scope.Name, unknown)
	result, err := s.CatchUp(context.Background(), "req_excluded", scope.ID, catchUpWindow(t), 20, "")
	page := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if !page.Partial || page.Scope.ExcludedPeers != 1 || len(page.Scope.CatchUp.Peers) != 0 || page.Freshness.Telegram != model.FreshnessUnavailable || strings.Contains(string(result.JSON), unknown.String()) || len(f.queries) != 0 {
		t.Fatal("denied peer exposed")
	}
}

// Adding optional date bindings must not change ordinary search cursor bytes.
func TestSearchCursorEncodingOmitsDateFields(t *testing.T) {
	encoded, err := json.Marshal(scopeCursorBinding{Operation: "search_messages", Scope: model.ScopeID("tgscope:v1:0123456789abcdef0123456789abcdef")})
	if err != nil || strings.Contains(string(encoded), "since") || strings.Contains(string(encoded), "until") {
		t.Fatal("search cursor encoding changed")
	}
}

func TestCatchUpStopsAtLowerDateBoundaryWithoutCallingOlderPages(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	old := candidate(grants[0], 20, "outside window")
	old.SentAt = catchUpWindow(t).Since - 1
	old.Message.Date = time.Unix(old.SentAt, 0).UTC().Format(time.RFC3339)
	f.messages[grants[0].Peer] = []model.Candidate{old}
	result, err := s.CatchUp(context.Background(), "req_boundary", scope.ID, catchUpWindow(t), 1, "")
	first := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(first.Items) != 0 || first.Scope.CompletedPeers != 1 || first.NextCursor == nil || first.Scope.CatchUp.Peers[0].Fetched != 1 {
		t.Fatal("lower boundary did not complete the peer")
	}
	result, err = s.CatchUp(context.Background(), "req_next", scope.ID, catchUpWindow(t), 1, *first.NextCursor)
	last := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if last.NextCursor != nil || last.Partial || len(f.queries) != 2 || f.queries[1].Peer != grants[1].Peer {
		t.Fatal("fetched beyond lower boundary")
	}
}
