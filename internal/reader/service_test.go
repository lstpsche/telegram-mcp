package reader

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

type fakeBackend struct {
	self                              model.PeerID
	items                             []model.Candidate
	historyCalls, chatCalls, ackCalls int
	through                           int32
	query                             model.HistoryQuery
	historyError, ackError            error
	onHistory                         func()
	onAck                             func()
	notReady                          bool
	searchQuery                       model.SearchQuery
	unreadCalls                       int
	unread                            model.Unread
	unreadError                       error
	onUnread                          func()
}

func (f *fakeBackend) Ready() bool          { return !f.notReady }
func (f *fakeBackend) SelfID() model.PeerID { return f.self }
func (f *fakeBackend) Chat(_ context.Context, peer model.PeerID) (model.Chat, error) {
	f.chatCalls++
	return model.Chat{ID: peer, Title: "Synthetic chat"}, nil
}
func (f *fakeBackend) History(_ context.Context, q model.HistoryQuery) ([]model.Candidate, error) {
	f.historyCalls++
	f.query = q
	if f.onHistory != nil {
		f.onHistory()
	}
	return f.items, f.historyError
}
func (f *fakeBackend) Acknowledge(_ context.Context, _ model.PeerID, through int32) error {
	f.ackCalls++
	f.through = through
	if f.onAck != nil {
		f.onAck()
	}
	return f.ackError
}

func testService(t *testing.T) (*Service, *fakeBackend, *policy.Repository, *sql.DB, policy.Grant) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dir, "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r, err := store.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SaveConfig(ctx, store.AccountConfig{APIID: 1, Environment: store.TestEnvironment, TestDC: 2, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordAuthorization(ctx, strings.Repeat("e", 43), nil, 2, now); err != nil {
		t.Fatal(err)
	}
	p, err := policy.New(db, filepath.Join(dir, "policy.lock"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := model.NewPeerID(model.PeerKindChat, 42)
	author, _ := model.NewPeerID(model.PeerKindUser, 7)
	grant := policy.Grant{Peer: peer, Author: author, MinID: 10, MaxID: 30, ReadThrough: 30, Profile: policy.ProfileSelfAuthored, ExpiresAt: now.Add(time.Hour), Eligible: true}
	f := &fakeBackend{self: author}
	s, err := New(f, p, func() time.Time { return now }, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return s, f, p, db, grant
}
func saveGrant(t *testing.T, p *policy.Repository, g policy.Grant) {
	t.Helper()
	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Save(context.Background(), g); err != nil {
		t.Fatal(err)
	}
}
func candidate(g policy.Grant, id int32, text string) model.Candidate {
	message, _ := model.NewMessageID(g.Peer, id)
	return model.Candidate{SentAt: 1788609600, Message: model.Message{ID: message, Author: g.Author, Date: "2026-09-05T12:00:00Z", Text: text}}
}
func request(g policy.Grant) model.HistoryQuery { return model.HistoryQuery{Peer: g.Peer, Limit: 20} }

func TestDeniedPeerHasNoTelegramIO(t *testing.T) {
	s, f, _, db, g := testService(t)
	result, err := s.Messages(context.Background(), "req_denied", request(g))
	if model.TextErrorCategory(err) != model.ErrorPolicyDenied || len(result.JSON) != 0 || f.historyCalls != 0 || f.ackCalls != 0 {
		t.Fatalf("denial did not precede I/O: %v", err)
	}
	var outcome string
	if err := db.QueryRow("SELECT outcome FROM text_audit").Scan(&outcome); err != nil || outcome != "failure" {
		t.Fatalf("audit: %s %v", outcome, err)
	}
}

func TestHistoryFiltersBodiesButAuthorizesWholePrefix(t *testing.T) {
	for _, ceiling := range []int32{19, 20} {
		t.Run(string(rune('a'+ceiling)), func(t *testing.T) {
			s, f, p, _, g := testService(t)
			g.ReadThrough = ceiling
			saveGrant(t, p, g)
			other, _ := model.NewPeerID(model.PeerKindUser, 9)
			hidden := candidate(g, 19, "excluded author")
			hidden.Message.Author = other
			f.items = []model.Candidate{candidate(g, 20, "untrusted <instructions> & \"quoted\""), hidden, candidate(g, 18, "eligible")}
			result, err := s.Messages(context.Background(), "req_interleaved", request(g))
			if ceiling == 19 {
				if model.TextErrorCategory(err) != model.ErrorPolicyDenied || f.ackCalls != 0 || len(result.JSON) != 0 {
					t.Fatalf("prefix escaped authority: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.ackCalls != 1 || f.through != 20 || f.query.MinID != 10 || f.query.MaxID != 30 {
				t.Fatal("incorrect grant range/read boundary")
			}
			var envelope model.Envelope[model.Message]
			if err := json.Unmarshal(result.JSON, &envelope); err != nil {
				t.Fatal(err)
			}
			if len(envelope.Items) != 2 || !envelope.Partial || envelope.ReadEffect.Kind != model.ReadEffectHistoryMarkedRead || strings.Contains(string(result.JSON), "excluded author") {
				t.Fatal("incorrect content filtering or effect")
			}
		})
	}
}

func TestUnsafeCandidatesNeverReleaseBodies(t *testing.T) {
	for _, flag := range []string{"protected", "ephemeral", "forwarded", "quoted", "unsupported"} {
		t.Run(flag, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			saveGrant(t, p, g)
			c := candidate(g, 20, "never release")
			switch flag {
			case "protected":
				c.Protected = true
			case "ephemeral":
				c.Ephemeral = true
			case "forwarded":
				c.Forwarded = true
			case "quoted":
				c.Quoted = true
			case "unsupported":
				c.Unsupported = true
			}
			f.items = []model.Candidate{c}
			result, err := s.Messages(context.Background(), "req_unsafe", request(g))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(result.JSON), "never release") || f.ackCalls != 0 || !strings.Contains(string(result.JSON), `"items":[]`) {
				t.Fatal("unsafe candidate released or read")
			}
		})
	}
}

func TestAckFailurePreservesCauseAndReleasesNothing(t *testing.T) {
	s, f, p, db, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "never release")}
	cause := errors.New("synthetic private upstream error")
	f.ackError = cause
	result, err := s.Messages(context.Background(), "req_uncertain", request(g))
	if model.TextErrorCategory(err) != model.ErrorReadEffectUncertain || !errors.Is(err, cause) || len(result.JSON) != 0 || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe acknowledgment failure: %v", err)
	}
	var uncertain bool
	if err := db.QueryRow("SELECT uncertain FROM text_audit").Scan(&uncertain); err != nil || !uncertain {
		t.Fatal("audit lost uncertainty")
	}
}

func TestBudgetsFailBeforeAcknowledgment(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	// Escaping and the mirrored MCP representation must both count.
	f.items = []model.Candidate{candidate(g, 20, strings.Repeat("\"", 50000))}
	result, err := s.Messages(context.Background(), "req_budget", request(g))
	if model.TextErrorCategory(err) != model.ErrorResultTooLarge || f.ackCalls != 0 || len(result.JSON) != 0 {
		t.Fatalf("oversized result read upstream: %v", err)
	}
}

func TestRevocationIsSerializedWithRead(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "eligible")}
	f.onHistory = func() {
		lease, err := p.Acquire(context.Background())
		if lease != nil || !errors.Is(err, policy.ErrBusy) {
			t.Fatalf("revocation raced read lease: %v", err)
		}
	}
	if _, err := s.Messages(context.Background(), "req_serial", request(g)); err != nil {
		t.Fatal(err)
	}
	lease, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Revoke(context.Background(), g.Peer); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if _, err := s.Messages(context.Background(), "req_revoked", request(g)); model.TextErrorCategory(err) != model.ErrorPolicyDenied || f.historyCalls != 1 {
		t.Fatal("revoked grant reused")
	}
}

func TestExpiryDuringFetchFailsClosed(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "expired")}
	f.onHistory = func() { s.now = func() time.Time { return g.ExpiresAt } }
	result, err := s.Messages(context.Background(), "req_expired", request(g))
	if err == nil || f.ackCalls != 0 || len(result.JSON) != 0 {
		t.Fatal("expired body released")
	}
}

func TestContextRequiresEligibleTarget(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 19, "neighbor")}
	q := request(g)
	q.Target = 20
	q.BeforeCount = 1
	q.Limit = 2
	result, err := s.Messages(context.Background(), "req_target", q)
	if model.TextErrorCategory(err) != model.ErrorPolicyDenied || f.ackCalls != 0 || len(result.JSON) != 0 {
		t.Fatal("missing target returned neighbors")
	}
	f.items = []model.Candidate{candidate(g, 20, "target")}
	q.BeforeCount = 0
	q.Limit = 1
	if _, err := s.Messages(context.Background(), "req_exact", q); err != nil || f.ackCalls != 1 || f.through != 20 {
		t.Fatalf("zero-neighbor context failed: %v", err)
	}
}

func TestContextRejectsUnexpectedNewerNeighbor(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "target"), candidate(g, 21, "unexpected newer")}
	q := request(g)
	q.Target = 20
	q.BeforeCount = 1
	q.Limit = 2
	result, err := s.Messages(context.Background(), "req_neighbors", q)
	if model.TextErrorCategory(err) != model.ErrorInvalidReference || f.ackCalls != 0 || len(result.JSON) != 0 {
		t.Fatal("unexpected newer neighbor expanded read effect")
	}
}

func TestExpiryDuringAcknowledgmentReleasesNothing(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "expired")}
	f.onAck = func() { s.now = func() time.Time { return g.ExpiresAt } }
	result, err := s.Messages(context.Background(), "req_ack_expiry", request(g))
	if model.TextErrorCategory(err) != model.ErrorReadEffectUncertain || len(result.JSON) != 0 {
		t.Fatal("content released after consent expired")
	}
}

func TestAuditFailureAfterReadDiscardsPreparedContent(t *testing.T) {
	s, f, p, db, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "prepared")}
	f.onAck = func() { _ = db.Close() }
	result, err := s.Messages(context.Background(), "req_audit", request(g))
	if model.TextErrorCategory(err) != model.ErrorReadEffectUncertain || len(result.JSON) != 0 {
		t.Fatal("audit failure released prepared content")
	}
}

func TestListChatsOnlyFetchesCurrentGrants(t *testing.T) {
	s, f, p, _, g := testService(t)
	empty, err := s.ListChats(context.Background(), "req_empty", 20)
	if err != nil || !strings.Contains(string(empty.JSON), `"items":[]`) || f.chatCalls != 0 {
		t.Fatal("empty policy fetched chat metadata")
	}
	saveGrant(t, p, g)
	result, err := s.ListChats(context.Background(), "req_chats", 20)
	if err != nil || !strings.Contains(string(result.JSON), g.Peer.String()) || f.chatCalls != 1 || f.ackCalls != 0 {
		t.Fatalf("granted chat lookup failed: %v", err)
	}
}

func (f *fakeBackend) Search(ctx context.Context, q model.SearchQuery) ([]model.Candidate, error) {
	f.searchQuery = q
	return f.History(ctx, model.HistoryQuery{Peer: q.Peer, Before: q.Before, MinID: q.MinID, MaxID: q.MaxID, Limit: q.Limit})
}
func (f *fakeBackend) Unread(_ context.Context, peer model.PeerID) (model.Unread, error) {
	f.unreadCalls++
	if f.onUnread != nil {
		f.onUnread()
	}
	value := f.unread
	if value.Peer.String() == "" {
		value.Peer = peer
	}
	return value, f.unreadError
}
