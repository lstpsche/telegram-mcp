package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func setFullRead(t *testing.T, p *policy.Repository, enabled bool) {
	t.Helper()
	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SetFullRead(context.Background(), enabled); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

type fullReadBackend struct {
	*fakeBackend
	dialogCalls int
	dialog      func(context.Context, model.DialogPosition, int) (model.DialogPage, error)
}

func (f *fullReadBackend) Dialogs(ctx context.Context, p model.DialogPosition, limit int) (model.DialogPage, error) {
	f.dialogCalls++
	return f.dialog(ctx, p, limit)
}

func TestFullReadDeliversMultipleAuthorsAndNewMessagesWithoutGrants(t *testing.T) {
	s, f, p, _, g := testService(t)
	ctx := context.Background()
	setFullRead(t, p, true)
	first := candidate(g, 20, "first author")
	second := candidate(g, 35, "second author")
	second.Message.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
	f.items = []model.Candidate{first, second}
	result, err := s.Messages(ctx, "req_full_history", model.HistoryQuery{Peer: g.Peer, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[model.Message]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 2 || f.query.MinID != 1 || f.query.MaxID != 2147483647 || f.through != 35 || envelope.ReadEffect.Kind != model.ReadEffectHistoryMarkedRead {
		t.Fatal("full history contract", string(result.JSON), f.query)
	}
	setFullRead(t, p, false)
	calls := f.historyCalls
	denied, err := s.Messages(ctx, "req_full_disabled", model.HistoryQuery{Peer: g.Peer, Limit: 20})
	if !errors.Is(err, policy.ErrDenied) || len(denied.JSON) != 0 || f.historyCalls != calls {
		t.Fatal("disabled access fetched", err)
	}
}

func TestFullReadImagesAndModeChangesInvalidateHandles(t *testing.T) {
	s, f, p, g, _ := imageService(t)
	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Revoke(context.Background(), g.Peer); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	setFullRead(t, p, true)
	result, err := s.Search(context.Background(), "req_full_image", g.Peer, model.SearchFilter{Query: "caption"}, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	var found model.Envelope[model.SearchHit]
	if err := json.Unmarshal(result.JSON, &found); err != nil {
		t.Fatal(err)
	}
	if len(found.Items) != 1 || found.Items[0].Image == nil {
		t.Fatal("image not discovered")
	}
	handle := found.Items[0].Image.Handle
	opened, err := s.OpenImage(context.Background(), "req_full_open", handle)
	if err != nil || opened.Image == nil || !bytes.Equal(opened.Image.Data, f.data) {
		t.Fatal("full image failed", err)
	}
	downloads := f.downloads
	setFullRead(t, p, false)
	setFullRead(t, p, true)
	opened, err = s.OpenImage(context.Background(), "req_full_stale", handle)
	if err == nil || opened.Image != nil || f.downloads != downloads {
		t.Fatal("old handle survived mode revision")
	}
}

func TestFullReadScopeUsesMembersWithoutDiscovery(t *testing.T) {
	s, f, p, _, g := testService(t)
	ctx := context.Background()
	setFullRead(t, p, true)
	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := l.SaveScope(ctx, "", "selected", []model.PeerID{g.Peer})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := s.ListScopes(ctx, "req_full_scopes")
	if err != nil {
		t.Fatal(err)
	}
	var scopes model.Envelope[scopeInfo]
	if err := json.Unmarshal(result.JSON, &scopes); err != nil {
		t.Fatal(err)
	}
	if len(scopes.Items) != 1 || scopes.Items[0].EligiblePeers != 1 || f.chatCalls != 0 {
		t.Fatal("scope authority lost")
	}
	f.items = []model.Candidate{candidate(g, 20, "synthetic")}
	if _, err := s.SearchScope(ctx, "req_full_scope_search", scope.ID, model.SearchFilter{Query: "synthetic"}, 20, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListChats(ctx, "req_full_scope_chats", 20, scope.ID); err != nil {
		t.Fatal(err)
	}
	if f.chatCalls != 1 {
		t.Fatal("scope fetched unexpected peers")
	}
}

func TestFullReadDialogPaginationBindingAndRevocation(t *testing.T) {
	for _, change := range []string{"continue", "tamper", "operation", "limit", "key", "expired", "mode", "scope"} {
		t.Run(change, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			ctx := context.Background()
			setFullRead(t, p, true)
			position := model.DialogPosition{Peer: g.Peer.String(), MessageID: 20, Date: 100, Pinned: true}
			backend := &fullReadBackend{fakeBackend: f}
			backend.dialog = func(_ context.Context, pos model.DialogPosition, limit int) (model.DialogPage, error) {
				if pos == (model.DialogPosition{}) {
					return model.DialogPage{Scanned: 1, Next: &position}, nil
				}
				if pos != position {
					t.Fatal("wrong cursor position", pos)
				}
				return model.DialogPage{Scanned: 1, Items: []model.DialogEntry{{Chat: model.Chat{ID: g.Peer, Title: "synthetic"}, Unread: model.Unread{Peer: g.Peer, Count: 2}}}}, nil
			}
			s.backend = backend
			result, err := s.Chats(ctx, "req_full_page", 1, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			var page model.Envelope[model.Chat]
			if err := json.Unmarshal(result.JSON, &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 0 || page.NextCursor == nil || !page.Partial {
				t.Fatal("excluded page lost continuation")
			}
			token := *page.NextCursor
			limit := 1
			switch change {
			case "tamper":
				token += "x"
			case "operation":
				_, err := s.UnreadPage(ctx, "req_wrong_operation", nil, token)
				if err == nil || backend.dialogCalls != 1 {
					t.Fatal("cross-operation cursor fetched")
				}
				return
			case "limit":
				limit = 2
			case "key":
				s.cursorKey[0]++
			case "expired":
				now := s.now().Add(cursorLifetime)
				s.now = func() time.Time { return now }
			case "mode":
				setFullRead(t, p, false)
				setFullRead(t, p, true)
			case "scope":
				l, err := p.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_, err = l.SaveScope(ctx, "", "changed", nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := l.Close(); err != nil {
					t.Fatal(err)
				}
			}
			result, err = s.Chats(ctx, "req_full_next", limit, nil, token)
			if change != "continue" {
				if err == nil || len(result.JSON) != 0 || backend.dialogCalls != 1 {
					t.Fatal("invalid token fetched", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(result.JSON, &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 1 || page.NextCursor != nil || backend.dialogCalls != 2 {
				t.Fatal("continuation failed")
			}
		})
	}
}

func TestFullReadDialogFailureDiscardsResultsAndHoldsLease(t *testing.T) {
	for _, failure := range []string{"backend", "invalid", "audit", "revocation"} {
		t.Run(failure, func(t *testing.T) {
			s, f, p, db, g := testService(t)
			setFullRead(t, p, true)
			backend := &fullReadBackend{fakeBackend: f, dialog: func(ctx context.Context, _ model.DialogPosition, _ int) (model.DialogPage, error) {
				if _, err := p.Acquire(ctx); !errors.Is(err, policy.ErrBusy) {
					t.Fatal("discovery did not hold policy lease", err)
				}
				page := model.DialogPage{Scanned: 1, Items: []model.DialogEntry{{Chat: model.Chat{ID: g.Peer, Title: "synthetic"}, Unread: model.Unread{Peer: g.Peer}}}}
				if failure == "backend" {
					return page, errors.New("synthetic upstream failure")
				}
				if failure == "invalid" {
					page.Scanned = 0
				}
				return page, nil
			}}
			s.backend = backend
			if failure == "audit" {
				if _, err := db.Exec(`CREATE TRIGGER reject_full_audit BEFORE INSERT ON text_audit BEGIN SELECT RAISE(ABORT,'synthetic'); END`); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.Chats(context.Background(), "req_full_failure", 20, nil, "")
			if failure != "revocation" && (err == nil || len(result.JSON) != 0) {
				t.Fatal("failure released metadata", err)
			}
			if failure == "revocation" && err != nil {
				t.Fatal(err)
			}
			setFullRead(t, p, false)
		})
	}
}

func TestFullReadSearchCursorInvalidatedByModeChange(t *testing.T) {
	s, f, p, _, g := testService(t)
	setFullRead(t, p, true)
	f.items = []model.Candidate{candidate(g, 20, "synthetic")}
	result, err := s.Search(context.Background(), "req_full_search", g.Peer, model.SearchFilter{Query: "synthetic"}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	var page model.Envelope[model.SearchHit]
	if err := json.Unmarshal(result.JSON, &page); err != nil || page.NextCursor == nil {
		t.Fatal("cursor missing", err)
	}
	setFullRead(t, p, false)
	setFullRead(t, p, true)
	result, err = s.Search(context.Background(), "req_full_changed", g.Peer, model.SearchFilter{Query: "synthetic"}, 1, *page.NextCursor)
	if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 {
		t.Fatal("search token survived", err)
	}
	if strings.Contains(*page.NextCursor, "synthetic") {
		t.Fatal("raw query in token")
	}
}

func TestFullReadDialogReleaseRechecksAfterAudit(t *testing.T) {
	for _, operation := range []string{"chats", "unread"} {
		for _, loss := range []string{"none", "expiry", "cancel", "readiness"} {
			t.Run(operation+"/"+loss, func(t *testing.T) {
				s, f, p, db, g := testService(t)
				setFullRead(t, p, true)
				now := s.now()
				auditTime := now
				s.now = func() time.Time { return now }
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				backend := &fullReadBackend{fakeBackend: f, dialog: func(context.Context, model.DialogPosition, int) (model.DialogPage, error) {
					return model.DialogPage{Scanned: 1, Items: []model.DialogEntry{{Chat: model.Chat{ID: g.Peer}, Unread: model.Unread{Peer: g.Peer, Count: 1}}}}, nil
				}}
				s.backend = backend
				audited := false
				repository, err := policy.New(db, filepath.Join(t.TempDir(), "state", "policy.lock"), func() time.Time {
					if backend.dialogCalls > 0 {
						audited = true
						switch loss {
						case "expiry":
							now = auditTime.Add(cursorLifetime)
						case "cancel":
							cancel()
						case "readiness":
							f.notReady = true
						}
					}
					return auditTime
				})
				if err != nil {
					t.Fatal(err)
				}
				s.policy = repository
				var result Result
				if operation == "chats" {
					result, err = s.Chats(ctx, "req_full_release", 20, nil, "")
				} else {
					result, err = s.UnreadPage(ctx, "req_full_release", nil, "")
				}
				if !audited {
					t.Fatal("audit hook missed", err, backend.dialogCalls)
				}
				if loss == "none" {
					if err != nil || len(result.JSON) == 0 {
						t.Fatal("valid result rejected", err)
					}
				} else if err == nil || len(result.JSON) != 0 {
					t.Fatal("invalid result released", err)
				}
			})
		}
	}
}
