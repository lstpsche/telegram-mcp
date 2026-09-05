package reader

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type scopeBackend struct {
	*fakeBackend
	messages       map[model.PeerID][]model.Candidate
	queries        []model.SearchQuery
	chats, unreads []model.PeerID
	onSearch       func(context.Context, model.SearchQuery) error
	onMetadata     func(model.PeerID) error
}

func (f *scopeBackend) Search(ctx context.Context, query model.SearchQuery) ([]model.Candidate, error) {
	f.queries = append(f.queries, query)
	if f.onSearch != nil {
		if err := f.onSearch(ctx, query); err != nil {
			return nil, err
		}
	}
	items := make([]model.Candidate, 0)
	for _, item := range f.messages[query.Peer] {
		id := item.Message.ID.TelegramID()
		if id >= query.MinID && id <= query.MaxID && (query.Before == 0 || id < query.Before) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Message.ID.TelegramID() > items[j].Message.ID.TelegramID() })
	if len(items) > query.Limit {
		items = items[:query.Limit]
	}
	return items, nil
}

func (f *scopeBackend) Chat(ctx context.Context, peer model.PeerID) (model.Chat, error) {
	f.chats = append(f.chats, peer)
	if f.onMetadata != nil {
		if err := f.onMetadata(peer); err != nil {
			return model.Chat{}, err
		}
	}
	return f.fakeBackend.Chat(ctx, peer)
}

func (f *scopeBackend) Unread(ctx context.Context, peer model.PeerID) (model.Unread, error) {
	f.unreads = append(f.unreads, peer)
	if f.onMetadata != nil {
		if err := f.onMetadata(peer); err != nil {
			return model.Unread{}, err
		}
	}
	return model.Unread{Peer: peer, Count: 1}, nil
}

func saveScope(t *testing.T, repository *policy.Repository, id model.ScopeID, name string, peers ...model.PeerID) policy.Scope {
	t.Helper()
	lease, err := repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	scope, err := lease.SaveScope(context.Background(), id, name, peers)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func scopeService(t *testing.T) (*Service, *scopeBackend, *policy.Repository, *sql.DB, []policy.Grant, policy.Scope) {
	t.Helper()
	s, fake, repository, db, first := testService(t)
	second := first
	second.Peer, _ = model.NewPeerID(model.PeerKindChat, 100)
	// Canonical string order places 100 before 42, regardless of numeric IDs.
	grants := []policy.Grant{second, first}
	for _, grant := range grants {
		saveGrant(t, repository, grant)
	}
	f := &scopeBackend{fakeBackend: fake, messages: make(map[model.PeerID][]model.Candidate)}
	s.backend = f
	scope := saveScope(t, repository, "", "work", first.Peer, second.Peer)
	return s, f, repository, db, grants, scope
}

func decodeScopeEnvelope[T any](t *testing.T, result Result, err error) model.Envelope[T] {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[T]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatal(err)
	}
	if envelope.ReadEffect.Kind != model.ReadEffectNone {
		t.Fatal("scope lookup changed read state")
	}
	return envelope
}

func TestScopeSearchUsesPeerMajorOrderingAndIndependentAnchors(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	for _, grant := range grants {
		f.messages[grant.Peer] = []model.Candidate{candidate(grant, 10, "last"), candidate(grant, 25, "newest"), candidate(grant, 20, "middle")}
	}
	var token string
	var ids []string
	for pageIndex := 0; pageIndex < 3; pageIndex++ {
		result, err := s.SearchScope(context.Background(), "req_page", scope.ID, "q", 2, token)
		page := decodeScopeEnvelope[model.SearchHit](t, result, err)
		if len(page.Items) != 2 || page.Scope == nil || page.Scope.EligiblePeers != 2 {
			t.Fatalf("page=%+v", page)
		}
		for _, item := range page.Items {
			ids = append(ids, item.ID.String())
		}
		if pageIndex < 2 {
			if page.NextCursor == nil || !page.Partial {
				t.Fatal("continuation lost")
			}
			token = *page.NextCursor
		} else if page.NextCursor != nil || page.Partial || page.Scope.CompletedPeers != 2 {
			t.Fatal("terminal coverage mismatch")
		}
	}
	want := []string{"tgmsg:v1:chat:100:25", "tgmsg:v1:chat:100:20", "tgmsg:v1:chat:100:10", "tgmsg:v1:chat:42:25", "tgmsg:v1:chat:42:20", "tgmsg:v1:chat:42:10"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("order=%v", ids)
	}
	if len(f.queries) != 4 || f.queries[1].Before != 20 || f.queries[1].MaxID != 25 || f.queries[2].Limit != 1 || f.queries[2].Before != 0 || f.queries[2].MaxID != 30 || f.queries[3].Before != 25 || f.queries[3].MaxID != 25 || f.ackCalls != 0 {
		t.Fatalf("queries=%+v acknowledgments=%d", f.queries, f.ackCalls)
	}
}

func TestScopeSearchCountsFilteredCandidatesAndAdvancesEmptyWindows(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	hidden := candidate(grants[0], 20, "private excluded text")
	hidden.Protected = true
	f.messages[grants[0].Peer] = []model.Candidate{hidden}
	f.messages[grants[1].Peer] = []model.Candidate{candidate(grants[1], 10, "visible")}
	result, err := s.SearchScope(context.Background(), "req_filtered", scope.ID, "q", 1, "")
	page := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(page.Items) != 0 || page.NextCursor == nil || !page.Partial || len(f.queries) != 1 || page.Scope.CompletedPeers != 0 || strings.Contains(string(result.JSON), "private excluded text") {
		t.Fatal("filtered candidates escaped page budget")
	}
	result, err = s.SearchScope(context.Background(), "req_next", scope.ID, "q", 1, *page.NextCursor)
	page = decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(page.Items) != 1 || page.Items[0].ID.Peer() != grants[1].Peer || page.NextCursor != nil || page.Partial || page.Scope.QueriedPeers != 2 || page.Scope.CompletedPeers != 2 || len(f.queries) != 3 || f.queries[1].Before != 20 {
		t.Fatalf("empty window failed to advance: %+v", page)
	}
}

func TestScopeSearchTraversesAtMostBoundedMembership(t *testing.T) {
	s, f, repository, _, grants, scope := scopeService(t)
	peers := []model.PeerID{grants[0].Peer, grants[1].Peer}
	for index := 0; index < policy.MaximumScopePeers-2; index++ {
		grant := grants[0]
		grant.Peer, _ = model.NewPeerID(model.PeerKindChat, int64(200+index))
		saveGrant(t, repository, grant)
		peers = append(peers, grant.Peer)
	}
	saveScope(t, repository, scope.ID, scope.Name, peers...)
	result, err := s.SearchScope(context.Background(), "req_empty_members", scope.ID, "q", 1, "")
	page := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(f.queries) != policy.MaximumScopePeers || len(page.Items) != 0 || page.NextCursor != nil || page.Partial || page.Scope.CompletedPeers != policy.MaximumScopePeers {
		t.Fatalf("bounded traversal failed: %+v", page)
	}
}

func TestScopeSelectionExcludesUnauthorizedMembersBeforeIO(t *testing.T) {
	for _, mode := range []string{"missing", "revoked", "expired", "self_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			s, f, repository, db, grants, scope := scopeService(t)
			switch mode {
			case "missing":
				unknown, _ := model.NewPeerID(model.PeerKindChat, 99)
				scope = saveScope(t, repository, scope.ID, scope.Name, grants[0].Peer, unknown)
			case "revoked":
				lease, err := repository.Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := lease.Revoke(context.Background(), grants[1].Peer); err != nil {
					t.Fatal(err)
				}
				lease.Close()
			case "expired":
				if _, err := db.Exec("UPDATE text_grants SET expires_at=? WHERE peer=?", s.now().Add(-time.Second).Format(time.RFC3339Nano), grants[1].Peer.String()); err != nil {
					t.Fatal(err)
				}
			case "self_mismatch":
				grants[1].Author, _ = model.NewPeerID(model.PeerKindUser, 999)
				saveGrant(t, repository, grants[1])
			}
			result, err := s.ListScopes(context.Background(), "req_scopes")
			list := decodeScopeEnvelope[scopeInfo](t, result, err)
			if len(list.Items) != 1 || list.Items[0].Name != "work" || list.Items[0].TotalPeers != 2 || list.Items[0].EligiblePeers != 1 || list.Items[0].ExcludedPeers != 1 || list.Freshness.Telegram != model.FreshnessUnavailable || strings.Contains(string(result.JSON), "tgpeer:") || len(f.queries)+len(f.chats)+len(f.unreads) != 0 {
				t.Fatalf("scope metadata=%+v", list)
			}
			result, err = s.ListChats(context.Background(), "req_chats", 20, scope.ID)
			chats := decodeScopeEnvelope[model.Chat](t, result, err)
			result, err = s.ListUnread(context.Background(), "req_unread", scope.ID)
			unread := decodeScopeEnvelope[model.Unread](t, result, err)
			result, err = s.SearchScope(context.Background(), "req_search", scope.ID, "q", 20, "")
			search := decodeScopeEnvelope[model.SearchHit](t, result, err)
			for _, coverage := range []*model.ScopeCoverage{chats.Scope, unread.Scope, search.Scope} {
				if coverage == nil || coverage.TotalPeers != 2 || coverage.EligiblePeers != 1 || coverage.ExcludedPeers != 1 || coverage.QueriedPeers != 1 || coverage.CompletedPeers != 1 {
					t.Fatalf("coverage=%+v", coverage)
				}
			}
			if !chats.Partial || !unread.Partial || !search.Partial || len(f.chats) != 1 || len(f.unreads) != 1 || len(f.queries) != 1 || f.chats[0] != grants[0].Peer || f.unreads[0] != grants[0].Peer || f.queries[0].Peer != grants[0].Peer || f.ackCalls != 0 {
				t.Fatal("excluded peer reached backend")
			}
		})
	}
}

func TestEmptyScopeAndUnknownScopeDoNotFetch(t *testing.T) {
	s, f, repository, _, _, scope := scopeService(t)
	saveScope(t, repository, scope.ID, scope.Name)
	for _, unknown := range []bool{false, true} {
		id := scope.ID
		if unknown {
			id = model.ScopeID("tgscope:v1:0123456789abcdef0123456789abcdef")
		}
		operations := []func() (Result, error){
			func() (Result, error) { return s.ListChats(context.Background(), "req_empty", 20, id) },
			func() (Result, error) { return s.ListUnread(context.Background(), "req_empty", id) },
			func() (Result, error) { return s.SearchScope(context.Background(), "req_empty", id, "q", 20, "") },
		}
		for _, operation := range operations {
			result, err := operation()
			if unknown {
				if model.TextErrorCategory(err) != model.ErrorInvalidReference || len(result.JSON) != 0 {
					t.Fatal("unknown scope returned an empty success")
				}
			} else if err != nil || !strings.Contains(string(result.JSON), `"items":[]`) || !strings.Contains(string(result.JSON), `"total_peers":0`) || !strings.Contains(string(result.JSON), `"telegram":"unavailable"`) {
				t.Fatalf("empty scope=%s err=%v", result.JSON, err)
			}
		}
	}
	if len(f.queries)+len(f.chats)+len(f.unreads) != 0 {
		t.Fatal("empty or unknown selection fetched")
	}
}

func TestFullyExcludedScopeHasNoLiveFreshnessClaim(t *testing.T) {
	s, f, repository, _, _, scope := scopeService(t)
	unknown, _ := model.NewPeerID(model.PeerKindChat, 99)
	saveScope(t, repository, scope.ID, scope.Name, unknown)
	operations := []func() (Result, error){
		func() (Result, error) { return s.ListChats(context.Background(), "req_excluded", 20, scope.ID) },
		func() (Result, error) { return s.ListUnread(context.Background(), "req_excluded", scope.ID) },
		func() (Result, error) {
			return s.SearchScope(context.Background(), "req_excluded", scope.ID, "q", 20, "")
		},
	}
	for _, operation := range operations {
		result, err := operation()
		page := decodeScopeEnvelope[json.RawMessage](t, result, err)
		if len(page.Items) != 0 || !page.Partial || page.Scope.ExcludedPeers != 1 || page.Scope.QueriedPeers != 0 || page.Freshness.Telegram != model.FreshnessUnavailable {
			t.Fatalf("excluded page=%+v", page)
		}
	}
	if len(f.queries)+len(f.chats)+len(f.unreads) != 0 {
		t.Fatal("fully excluded selection fetched")
	}
}

func TestScopedChatLimitHasExplicitCoverage(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	result, err := s.ListChats(context.Background(), "req_limited", 1, scope.ID)
	page := decodeScopeEnvelope[model.Chat](t, result, err)
	if len(page.Items) != 1 || page.Items[0].ID != grants[0].Peer || !page.Partial || page.Scope.EligiblePeers != 2 || page.Scope.QueriedPeers != 1 || page.Scope.CompletedPeers != 1 || len(f.chats) != 1 || page.NextCursor != nil {
		t.Fatalf("chat limit coverage=%+v", page)
	}
}

func TestScopedMetadataLateFailureDiscardsWholeResult(t *testing.T) {
	for _, operation := range []string{"chats", "unread"} {
		for _, failure := range []string{"upstream", "expiry", "readiness", "cancel", "audit"} {
			t.Run(operation+"_"+failure, func(t *testing.T) {
				s, f, _, db, grants, scope := scopeService(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				f.onMetadata = func(peer model.PeerID) error {
					if peer != grants[1].Peer {
						return nil
					}
					switch failure {
					case "upstream":
						return errors.New("private upstream metadata")
					case "expiry":
						s.now = func() time.Time { return grants[0].ExpiresAt }
					case "readiness":
						f.notReady = true
					case "cancel":
						cancel()
					case "audit":
						if _, err := db.Exec("DROP TABLE text_audit"); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				}
				var result Result
				var err error
				if operation == "chats" {
					result, err = s.ListChats(ctx, "req_failed", 20, scope.ID)
				} else {
					result, err = s.ListUnread(ctx, "req_failed", scope.ID)
				}
				if err == nil || len(result.JSON) != 0 || len(f.chats)+len(f.unreads) != 2 || f.ackCalls != 0 {
					t.Fatalf("result=%s err=%v", result.JSON, err)
				}
			})
		}
	}
}

func TestScopeListAuditFailureReleasesNoNames(t *testing.T) {
	s, f, _, db, _, _ := scopeService(t)
	if _, err := db.Exec("DROP TABLE text_audit"); err != nil {
		t.Fatal(err)
	}
	result, err := s.ListScopes(context.Background(), "req_failed")
	if err == nil || len(result.JSON) != 0 || len(f.queries)+len(f.chats)+len(f.unreads) != 0 {
		t.Fatalf("result=%s err=%v", result.JSON, err)
	}
}

func TestScopeCursorRejectsSignedInvalidTraversalState(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	f.messages[grants[0].Peer] = []model.Candidate{candidate(grants[0], 20, "match")}
	result, err := s.SearchScope(context.Background(), "req_start", scope.ID, "q", 1, "")
	page := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if page.NextCursor == nil {
		t.Fatal("missing cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(*page.NextCursor, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var original scopeSearchCursor
	if err := json.Unmarshal(payload, &original); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*scopeSearchCursor){
		func(c *scopeSearchCursor) { c.Index = -1 },
		func(c *scopeSearchCursor) { c.Index = len(grants) },
		func(c *scopeSearchCursor) { c.Before = -1 },
		func(c *scopeSearchCursor) { c.Before = grants[0].MinID },
		func(c *scopeSearchCursor) { c.Before = c.Ceiling + 1 },
		func(c *scopeSearchCursor) { c.Ceiling = grants[0].MaxID + 1 },
		func(c *scopeSearchCursor) { c.Ceiling = grants[0].MinID - 1 },
		func(c *scopeSearchCursor) { c.Expires = s.now().Add(cursorLifetime + time.Second).Unix() },
	} {
		cursor := original
		change(&cursor)
		token, err := s.encodeScopeCursor(cursor)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.SearchScope(context.Background(), "req_invalid", scope.ID, "q", 1, token)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || len(f.queries) != 1 {
			t.Fatalf("invalid traversal reached backend: %+v err=%v", cursor, err)
		}
	}
	// A valid signature must not permit a noncanonical encoding of its payload.
	encoded := base64.RawURLEncoding.EncodeToString(append([]byte(" "), payload...))
	token := "ss1." + encoded + "." + base64.RawURLEncoding.EncodeToString(s.scopeCursorSignature(encoded))
	result, err = s.SearchScope(context.Background(), "req_noncanonical", scope.ID, "q", 1, token)
	if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || len(f.queries) != 1 {
		t.Fatal("signed noncanonical cursor accepted")
	}
}

func TestScopeCursorBindingsInvalidateBeforeIO(t *testing.T) {
	for _, change := range []string{"query", "limit", "scope", "rename", "members", "delete_recreate", "regrant", "revoke_regrant", "epoch", "key", "tamper", "expired", "natural_expiry"} {
		t.Run(change, func(t *testing.T) {
			s, f, repository, db, grants, scope := scopeService(t)
			grants[1].ExpiresAt = s.now().Add(2 * time.Minute)
			saveGrant(t, repository, grants[1])
			f.messages[grants[0].Peer] = []model.Candidate{candidate(grants[0], 20, "match")}
			result, err := s.SearchScope(context.Background(), "req_start", scope.ID, "secret query", 1, "")
			page := decodeScopeEnvelope[model.SearchHit](t, result, err)
			if page.NextCursor == nil {
				t.Fatal("missing cursor")
			}
			token := *page.NextCursor
			payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
			if err != nil {
				t.Fatal(err)
			}
			var cursor scopeSearchCursor
			if err := json.Unmarshal(payload, &cursor); err != nil {
				t.Fatal(err)
			}
			if cursor.Expires != grants[1].ExpiresAt.Unix() || strings.Contains(string(payload), "secret query") || strings.Contains(string(payload), "tgpeer:") {
				t.Fatal("cursor expiry or confidentiality mismatch")
			}
			query, limit, id := "secret query", 1, scope.ID
			switch change {
			case "query":
				query = "different"
			case "limit":
				limit = 2
			case "scope":
				id = saveScope(t, repository, "", "other", grants[0].Peer).ID
			case "rename":
				saveScope(t, repository, scope.ID, "renamed", scope.Peers...)
			case "members":
				saveScope(t, repository, scope.ID, scope.Name, grants[0].Peer)
			case "delete_recreate":
				lease, err := repository.Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := lease.DeleteScope(context.Background(), scope.ID); err != nil {
					t.Fatal(err)
				}
				lease.Close()
				id = saveScope(t, repository, "", scope.Name, scope.Peers...).ID
			case "regrant":
				saveGrant(t, repository, grants[0])
			case "revoke_regrant":
				lease, err := repository.Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := lease.Revoke(context.Background(), grants[0].Peer); err != nil {
					t.Fatal(err)
				}
				lease.Close()
				saveGrant(t, repository, grants[0])
			case "epoch":
				if _, err := db.Exec("UPDATE authorization_state SET epoch=?", strings.Repeat("n", 43)); err != nil {
					t.Fatal(err)
				}
			case "key":
				s.cursorKey[0]++
			case "tamper":
				token += "!"
			case "expired":
				now := s.now().Add(cursorLifetime)
				s.now = func() time.Time { return now }
			case "natural_expiry":
				s.now = func() time.Time { return grants[1].ExpiresAt }
			}
			result, err = s.SearchScope(context.Background(), "req_changed", id, query, limit, token)
			if err == nil || len(result.JSON) != 0 || len(f.queries) != 1 || f.ackCalls != 0 {
				t.Fatalf("binding %s reached I/O: %v", change, err)
			}
		})
	}
}

func TestScopeSearchLateFailureDropsEarlierPeerResults(t *testing.T) {
	for _, failure := range []string{"upstream", "expiry", "readiness", "cancel", "audit", "cursor_expiry"} {
		t.Run(failure, func(t *testing.T) {
			s, f, _, db, grants, scope := scopeService(t)
			for _, grant := range grants {
				f.messages[grant.Peer] = []model.Candidate{candidate(grant, 20, "private result")}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("private upstream failure")
			f.onSearch = func(_ context.Context, query model.SearchQuery) error {
				if query.Peer != grants[1].Peer {
					return nil
				}
				switch failure {
				case "upstream":
					return cause
				case "expiry":
					s.now = func() time.Time { return grants[0].ExpiresAt }
				case "readiness":
					f.notReady = true
				case "cancel":
					cancel()
				case "audit":
					_, err := db.Exec("DROP TABLE text_audit")
					if err != nil {
						t.Fatal(err)
					}
				case "cursor_expiry":
					now := s.now().Add(cursorLifetime)
					s.now = func() time.Time { return now }
				}
				return nil
			}
			result, err := s.SearchScope(ctx, "req_failure", scope.ID, "q", 20, "")
			if err == nil || len(result.JSON) != 0 || len(f.queries) != 2 || f.ackCalls != 0 {
				t.Fatalf("failure=%s result=%s err=%v", failure, result.JSON, err)
			}
			if failure == "upstream" && !errors.Is(err, cause) {
				t.Fatal("upstream cause lost")
			}
		})
	}
}

func TestScopeCursorContinuesAfterReaderReconstruction(t *testing.T) {
	s, backend, repository, _, grants, scope := scopeService(t)
	backend.messages[grants[0].Peer] = []model.Candidate{candidate(grants[0], 10, "first peer")}
	for _, id := range []int32{25, 20, 15, 10} {
		backend.messages[grants[1].Peer] = append(backend.messages[grants[1].Peer], candidate(grants[1], id, "second peer"))
	}
	result, err := s.SearchScope(context.Background(), "req_original", scope.ID, "q", 2, "")
	first := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if first.NextCursor == nil || len(first.Items) != 2 || first.Scope.CompletedPeers != 1 {
		t.Fatalf("initial traversal=%+v", first)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(*first.NextCursor, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var saved scopeSearchCursor
	if err := json.Unmarshal(payload, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Index != 1 || saved.Before != 25 || saved.Ceiling != 25 {
		t.Fatalf("saved traversal=%+v", saved)
	}
	// Reconstruction has no in-memory cursor state and cannot extend its lifetime.
	now := s.now().Add(time.Minute)
	restarted, err := New(backend, repository, func() time.Time { return now }, s.cursorKey[:])
	if err != nil {
		t.Fatal(err)
	}
	result, err = restarted.SearchScope(context.Background(), "req_reconstructed", scope.ID, "q", 2, *first.NextCursor)
	second := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if second.NextCursor == nil || len(second.Items) != 2 || second.Scope.CompletedPeers != 1 || len(backend.queries) != 3 {
		t.Fatalf("resumed traversal=%+v", second)
	}
	query := backend.queries[2]
	if query.Peer != grants[1].Peer || query.Before != saved.Before || query.MaxID != saved.Ceiling {
		t.Fatalf("resumed query=%+v", query)
	}
	continued, err := restarted.decodeScopeCursor(*second.NextCursor, saved.Binding)
	if err != nil || continued.Index != saved.Index || continued.Before != 15 || continued.Ceiling != saved.Ceiling || continued.Expires != saved.Expires {
		t.Fatalf("continued cursor=%+v err=%v", continued, err)
	}
	result, err = restarted.SearchScope(context.Background(), "req_terminal", scope.ID, "q", 2, *second.NextCursor)
	last := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if last.NextCursor != nil || len(last.Items) != 1 || last.Scope.CompletedPeers != 2 || last.Partial {
		t.Fatalf("terminal page=%+v", last)
	}
	var ids []string
	for _, page := range []model.Envelope[model.SearchHit]{first, second, last} {
		for _, item := range page.Items {
			ids = append(ids, item.ID.String())
		}
	}
	want := []string{"tgmsg:v1:chat:100:10", "tgmsg:v1:chat:42:25", "tgmsg:v1:chat:42:20", "tgmsg:v1:chat:42:15", "tgmsg:v1:chat:42:10"}
	if fmt.Sprint(ids) != fmt.Sprint(want) || backend.ackCalls != 0 {
		t.Fatalf("reconstructed traversal repeated or skipped results: %v", ids)
	}
}

func FuzzScopeCursorCanonicalValidation(f *testing.F) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	s := &Service{now: func() time.Time { return now }}
	copy(s.cursorKey[:], strings.Repeat("k", 32))
	binding := scopeCursorBinding{Operation: "search_messages", Scope: model.ScopeID("tgscope:v1:0123456789abcdef0123456789abcdef"), MembersDigest: "synthetic-members-digest", QueryDigest: s.queryDigest("q"), Limit: 2, Epoch: strings.Repeat("e", 43), Revision: 1}
	cursor := scopeSearchCursor{Binding: binding, Index: 1, Before: 20, Ceiling: 25, Expires: now.Add(time.Minute).Unix()}
	payload, err := json.Marshal(cursor)
	if err != nil {
		f.Fatal(err)
	}
	token, err := s.encodeScopeCursor(cursor)
	if err != nil {
		f.Fatal(err)
	}
	for _, value := range []string{string(payload), " " + string(payload), strings.Replace(string(payload), `"index":1`, `"index":1,"index":0`, 1), strings.Replace(string(payload), `"index":1`, `"index":-1`, 1), `{}`, `null`, string([]byte{255}), strings.Repeat("x", 4097)} {
		f.Add(value, true)
	}
	for _, value := range []string{token, "", token + "=", "ss1.invalid.invalid", strings.Repeat("x", 4097)} {
		f.Add(value, false)
	}
	f.Fuzz(func(t *testing.T, value string, signed bool) {
		if len(value) > 8192 {
			t.Skip()
		}
		token := value
		if signed {
			encoded := base64.RawURLEncoding.EncodeToString([]byte(value))
			token = "ss1." + encoded + "." + base64.RawURLEncoding.EncodeToString(s.scopeCursorSignature(encoded))
		}
		decoded, err := s.decodeScopeCursor(token, binding)
		if err != nil {
			if category := model.TextErrorCategory(err); category != model.ErrorCursorInvalid && category != model.ErrorCursorExpired {
				t.Fatalf("unexpected error category: %v", category)
			}
			return
		}
		if decoded.Binding != binding || decoded.Index < 0 || decoded.Index >= policy.MaximumScopePeers || decoded.Before < 0 || decoded.Ceiling <= 0 || decoded.Before > decoded.Ceiling || decoded.Expires <= now.Unix() || decoded.Expires > now.Add(cursorLifetime).Unix() {
			t.Fatal("invalid state accepted")
		}
		canonical, err := s.encodeScopeCursor(decoded)
		if err != nil || canonical != token {
			t.Fatal("noncanonical cursor accepted")
		}
	})
}
