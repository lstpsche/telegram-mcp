package reader

import (
	"context"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func TestSearchRestartUsesLiveResultsAndReauthorizesContext(t *testing.T) {
	for _, change := range []string{"edited", "protected", "deleted"} {
		t.Run(change, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			saveGrant(t, p, g)
			ctx := context.Background()
			f.items = []model.Candidate{candidate(g, 20, "previous text"), candidate(g, 18, "older")}
			result, err := s.Search(ctx, "req_search", g.Peer, "q", 2, "")
			page := searchEnvelope(t, result, err)
			if page.NextCursor == nil {
				t.Fatal("missing continuation")
			}
			restarted, err := New(f, p, s.now, s.cursorKey[:])
			if err != nil {
				t.Fatal(err)
			}
			f.items = []model.Candidate{candidate(g, 17, "changed older match")}
			result, err = restarted.Search(ctx, "req_continue", g.Peer, "q", 2, *page.NextCursor)
			next := searchEnvelope(t, result, err)
			if len(next.Items) != 1 || next.Items[0].Snippet != "changed older match" || f.searchQuery.Before != 18 || f.searchQuery.MaxID != 20 || f.ackCalls != 0 {
				t.Fatal("restart lost scope or reused stale results")
			}
			f.items = []model.Candidate{candidate(g, 20, "edited live body")}
			switch change {
			case "protected":
				f.items[0].Protected = true
			case "deleted":
				f.items = nil
			}
			result, err = restarted.Messages(ctx, "req_context", model.HistoryQuery{Peer: g.Peer, Target: 20, Limit: 1})
			if change == "edited" {
				if err != nil || !strings.Contains(string(result.JSON), "edited live body") || strings.Contains(string(result.JSON), "previous text") || f.ackCalls != 1 {
					t.Fatal("context did not refetch the edited body", err)
				}
			} else if err == nil || len(result.JSON) != 0 || f.ackCalls != 0 {
				t.Fatal("unavailable target released text or caused a receipt")
			}
			if f.historyCalls != 3 {
				t.Fatal("result was served without a live fetch")
			}
		})
	}
}

func TestAuthorizationLifecycleClearsRecoveryMetadataAndCursorAuthority(t *testing.T) {
	for _, action := range []string{"rotate", "delete"} {
		t.Run(action, func(t *testing.T) {
			s, f, p, db, g := testService(t)
			ctx := context.Background()
			saveGrant(t, p, g)
			f.items = []model.Candidate{candidate(g, 20, "match")}
			result, err := s.Search(ctx, "req_search", g.Peer, "q", 1, "")
			page := searchEnvelope(t, result, err)
			if page.NextCursor == nil {
				t.Fatal("missing cursor")
			}
			for _, sql := range []string{
				"INSERT INTO telegram_update_state(epoch,user_id,pts,qts,date,seq) SELECT epoch,7,10,0,100,1 FROM authorization_state",
				"INSERT INTO telegram_peer_hashes(epoch,user_id,kind,peer_id,access_hash) SELECT epoch,7,'user',8,22 FROM authorization_state",
				"INSERT INTO telegram_channel_state(epoch,user_id,channel_id,pts) SELECT epoch,7,9,10 FROM authorization_state",
			} {
				if _, err := db.Exec(sql); err != nil {
					t.Fatal(err)
				}
			}
			repo, err := store.NewRepository(db)
			if err != nil {
				t.Fatal(err)
			}
			if action == "rotate" {
				err = repo.RecordAuthorization(ctx, strings.Repeat("n", 43), nil, 2, s.now())
			} else {
				err = repo.InvalidateAuthorization(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"text_grants", "telegram_update_state", "telegram_peer_hashes", "telegram_channel_state"} {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("authorization lifecycle retained "+table, err)
				}
			}
			if action == "rotate" {
				saveGrant(t, p, g)
			}
			restarted, err := New(f, p, s.now, s.cursorKey[:])
			if err != nil {
				t.Fatal(err)
			}
			result, err = restarted.Search(ctx, "req_stale", g.Peer, "q", 1, *page.NextCursor)
			if err == nil || len(result.JSON) != 0 || f.historyCalls != 1 || f.ackCalls != 0 {
				t.Fatal("old cursor authorized a fetch after account lifecycle change")
			}
		})
	}
}
