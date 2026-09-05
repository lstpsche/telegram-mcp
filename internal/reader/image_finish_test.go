package reader

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func TestOpenImageRejectsAuthorityLossDuringAudit(t *testing.T) {
	for _, loss := range []string{"expiry", "cancel", "readiness"} {
		t.Run(loss, func(t *testing.T) {
			s, backend, _, _, handle := imageService(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := s.now()
			auditTime := now
			s.now = func() time.Time { return now }
			auditHookRan := false
			repository, err := policy.New(backend.db, filepath.Join(t.TempDir(), "state", "policy.lock"), func() time.Time {
				// The repository reads its clock for grant lookup and audit. The
				// latter runs after the final exact-source check and receipt.
				if backend.ackCalls > 0 && !auditHookRan {
					auditHookRan = true
					switch loss {
					case "expiry":
						now = now.Add(imageLifetime)
					case "cancel":
						cancel()
					case "readiness":
						backend.notReady = true
					}
				}
				return auditTime
			})
			if err != nil {
				t.Fatal(err)
			}
			s.policy = repository
			result, err := s.OpenImage(ctx, "req_image_audit_release", handle)
			if !auditHookRan || backend.ackCalls != 1 || backend.downloads != 1 || backend.historyCalls != 3 {
				t.Fatalf("fixture did not reach audit after receipt: hook=%t ack=%d download=%d history=%d err=%v", auditHookRan, backend.ackCalls, backend.downloads, backend.historyCalls, err)
			}
			if model.TextErrorCategory(err) != model.ErrorReadEffectUncertain || result.Image != nil || len(result.JSON) != 0 {
				t.Fatalf("authority loss during audit released image: %v", err)
			}
			var auditCount int
			if err := backend.db.QueryRow("SELECT count(*) FROM text_audit WHERE request_id = 'req_image_audit_release'").Scan(&auditCount); err != nil || auditCount != 1 {
				t.Fatal("audit failure masked the release check", err)
			}
			lease, err := repository.Acquire(context.Background())
			if err != nil {
				t.Fatal("failed image delivery retained policy lease", err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
