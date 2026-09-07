package reader

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestCatchUpCheckpointAppearsOnlyAfterCompleteWindowAndResumes(t *testing.T) {
	s, f, p, _, grants, scope := scopeService(t)
	now := s.now().Add(10 * time.Second)
	s.now = func() time.Time { return now }
	w := catchUpWindow(t)
	f.messages[grants[0].Peer] = []model.Candidate{candidate(grants[0], 20, "private synthetic body")}
	result, err := s.CatchUp(context.Background(), "req_checkpoint_start", scope.ID, w, 1, "")
	first := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if first.NextCursor == nil || first.Scope.CatchUp.Checkpoint != "" {
		t.Fatal("unfinished scan advanced checkpoint")
	}
	result, err = s.CatchUp(context.Background(), "req_checkpoint_finish", scope.ID, w, 1, *first.NextCursor)
	last := decodeScopeEnvelope[model.SearchHit](t, result, err)
	checkpoint := last.Scope.CatchUp.Checkpoint
	if last.NextCursor != nil || !strings.HasPrefix(checkpoint, "cu1.") {
		t.Fatal("completed scan missing checkpoint")
	}
	f.messages = make(map[model.PeerID][]model.Candidate)
	restarted, err := New(f, p, s.now, s.cursorKey[:])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err = restarted.CatchUpFrom(context.Background(), "req_checkpoint_resume", scope.ID, checkpoint, w.Until+2, 20, "")
		page := decodeScopeEnvelope[model.SearchHit](t, result, err)
		if page.Scope.CatchUp.Since != time.Unix(w.Until, 0).UTC().Format(time.RFC3339) || page.Scope.CatchUp.Checkpoint == "" || f.ackCalls != 0 {
			t.Fatal("resume lost boundary or issued receipt")
		}
	}
}

func TestCatchUpCheckpointBindingRejectsBeforeFetching(t *testing.T) {
	for _, change := range []string{"policy", "scope", "key", "tamper", "expired", "backwards", "cursor_as_checkpoint"} {
		t.Run(change, func(t *testing.T) {
			s, f, p, _, grants, scope := scopeService(t)
			now := s.now().Add(10 * time.Second)
			s.now = func() time.Time { return now }
			w := catchUpWindow(t)
			result, err := s.CatchUp(context.Background(), "req_checkpoint", scope.ID, w, 20, "")
			checkpoint := decodeScopeEnvelope[model.SearchHit](t, result, err).Scope.CatchUp.Checkpoint
			until := w.Until + 1
			switch change {
			case "policy":
				saveGrant(t, p, grants[0])
			case "scope":
				scope = saveScope(t, p, "", "other", grants[0].Peer)
			case "key":
				s.cursorKey[0]++
			case "tamper":
				checkpoint += "x"
			case "expired":
				now = now.Add(31 * 24 * time.Hour)
			case "backwards":
				until = w.Until
			case "cursor_as_checkpoint":
				checkpoint = "ss1." + strings.TrimPrefix(checkpoint, "cu1.")
			}
			calls := len(f.queries)
			result, err = s.CatchUpFrom(context.Background(), "req_checkpoint_invalid", scope.ID, checkpoint, until, 20, "")
			if err == nil || len(result.JSON) != 0 || len(f.queries) != calls {
				t.Fatal("invalid checkpoint fetched or released")
			}
		})
	}
}

func TestCatchUpCheckpointPaginationKeepsOriginalCheckpointAndUntil(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	now := s.now().Add(10 * time.Second)
	s.now = func() time.Time { return now }
	w := catchUpWindow(t)
	result, err := s.CatchUp(context.Background(), "req_checkpoint_base", scope.ID, w, 20, "")
	checkpoint := decodeScopeEnvelope[model.SearchHit](t, result, err).Scope.CatchUp.Checkpoint
	item := candidate(grants[0], 20, "new message")
	item.Message.Date = time.Unix(w.Until, 0).UTC().Format(time.RFC3339)
	item.SentAt = w.Until
	f.messages[grants[0].Peer] = []model.Candidate{item}
	result, err = s.CatchUpFrom(context.Background(), "req_checkpoint_page", scope.ID, checkpoint, w.Until+2, 1, "")
	first := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if first.NextCursor == nil || first.Scope.CatchUp.Checkpoint != "" || len(first.Items) != 1 {
		t.Fatal("bad resumed continuation")
	}
	calls := len(f.queries)
	if result, err := s.CatchUpFrom(context.Background(), "req_checkpoint_changed", scope.ID, checkpoint, w.Until+3, 1, *first.NextCursor); err == nil || len(result.JSON) != 0 || len(f.queries) != calls {
		t.Fatal("changed until resumed cursor")
	}
	result, err = s.CatchUpFrom(context.Background(), "req_checkpoint_next", scope.ID, checkpoint, w.Until+2, 1, *first.NextCursor)
	last := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if last.NextCursor != nil || last.Scope.CatchUp.Checkpoint == "" {
		t.Fatal("resumed scan did not complete")
	}
}

func TestCatchUpFutureWindowDoesNotIssueCheckpoint(t *testing.T) {
	s, _, _, _, _, scope := scopeService(t)
	result, err := s.CatchUp(context.Background(), "req_future", scope.ID, catchUpWindow(t), 20, "")
	page := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if page.Scope.CatchUp.Checkpoint != "" {
		t.Fatal("future messages could be skipped by checkpoint")
	}
}

func TestCatchUpCheckpointAuditFailureWithholdsCompletion(t *testing.T) {
	s, f, _, db, _, scope := scopeService(t)
	now := s.now().Add(10 * time.Second)
	s.now = func() time.Time { return now }
	if _, err := db.Exec("CREATE TRIGGER deny_completion_audit BEFORE INSERT ON text_audit BEGIN SELECT RAISE(FAIL, 'synthetic'); END"); err != nil {
		t.Fatal(err)
	}
	result, err := s.CatchUp(context.Background(), "req_checkpoint_audit", scope.ID, catchUpWindow(t), 20, "")
	if err == nil || len(result.JSON) != 0 || f.ackCalls != 0 {
		t.Fatal("failed audit released checkpoint")
	}
}
