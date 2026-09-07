package reader

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func TestImageDiscoveryRechecksValidityAfterAudit(t *testing.T) {
	for _, operation := range []string{"messages", "context", "search", "scope_search"} {
		for _, loss := range []string{"none", "grant_expiry", "cancel", "readiness", "cursor_expiry"} {
			if loss == "cursor_expiry" && (operation == "messages" || operation == "context") {
				continue
			}
			t.Run(operation+"/"+loss, func(t *testing.T) {
				s, backend, originalPolicy, grant, _ := imageService(t)
				scope := saveScope(t, originalPolicy, "", "images", grant.Peer)
				backend.searchQuery = model.SearchQuery{}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				now := s.now()
				auditTime := now
				s.now = func() time.Time { return now }
				auditHookRan := false
				repository, err := policy.New(backend.db, filepath.Join(t.TempDir(), "state", "policy.lock"), func() time.Time {
					// Policy time is read before fetching and again when writing the
					// audit. Change validity only during that final audit write.
					if (backend.historyCalls > 0 || backend.searchQuery.Peer.String() != "") && !auditHookRan {
						auditHookRan = true
						switch loss {
						case "grant_expiry":
							now = grant.ExpiresAt
						case "cancel":
							cancel()
						case "readiness":
							backend.notReady = true
						case "cursor_expiry":
							now = now.Add(cursorLifetime)
						}
					}
					return auditTime
				})
				if err != nil {
					t.Fatal(err)
				}
				s.policy = repository
				const requestID = "req_image_discovery_release"
				var result Result
				switch operation {
				case "messages":
					result, err = s.Messages(ctx, requestID, request(grant))
				case "context":
					result, err = s.Messages(ctx, requestID, model.HistoryQuery{Peer: grant.Peer, Target: 20, Limit: 1})
				case "search":
					result, err = s.Search(ctx, requestID, grant.Peer, model.SearchFilter{Query: "caption"}, 20, "")
				case "scope_search":
					result, err = s.SearchScope(ctx, requestID, scope.ID, model.SearchFilter{Query: "caption"}, 20, "")
				}
				if !auditHookRan || backend.downloads != 0 {
					t.Fatal("fixture missed audit or discovery downloaded image bytes")
				}
				acknowledged := operation == "messages" || operation == "context"
				expectedReceipts := 0
				if acknowledged {
					expectedReceipts = 1
				}
				if backend.ackCalls != expectedReceipts {
					t.Fatal("unexpected discovery read effect")
				}
				if loss == "none" {
					var envelope model.Envelope[struct {
						Image *model.ImageDescriptor `json:"image"`
					}]
					if err != nil || json.Unmarshal(result.JSON, &envelope) != nil || len(envelope.Items) != 1 || envelope.Items[0].Image == nil {
						t.Fatalf("valid image discovery rejected during cleanup: %v", err)
					}
				} else {
					if err == nil || len(result.JSON) != 0 || result.Image != nil {
						t.Fatalf("invalid discovery released metadata: %v", err)
					}
					expectedCategory := map[string]model.ErrorCategory{
						"grant_expiry":  model.ErrorPolicyDenied,
						"cancel":        model.ErrorCancelled,
						"readiness":     model.ErrorFreshnessDegraded,
						"cursor_expiry": model.ErrorCursorExpired,
					}[loss]
					if acknowledged {
						expectedCategory = model.ErrorReadEffectUncertain
					}
					if model.TextErrorCategory(err) != expectedCategory {
						t.Fatalf("unexpected failure category: got %s want %s", model.TextErrorCategory(err), expectedCategory)
					}
				}
				var audits int
				if err := backend.db.QueryRow("SELECT count(*) FROM text_audit WHERE request_id = ?", requestID).Scan(&audits); err != nil || audits != 1 {
					t.Fatal("audit failure masked release validation", err)
				}
				lease, err := repository.Acquire(context.Background())
				if err != nil {
					t.Fatal("discovery retained policy lease", err)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
