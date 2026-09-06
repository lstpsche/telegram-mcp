package policy

import (
	"context"
	"errors"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"testing"
	"time"
)

func TestDocumentPermissionRequiresOptInAndPreservesContentBounds(t *testing.T) {
	now := time.Now()
	grant := validGrant(t, now)
	id, _ := model.NewMessageID(grant.Peer, 18)
	candidate := model.Candidate{Message: model.Message{ID: id, Author: grant.Author}, Document: &model.MediaSource{}}
	if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
		t.Fatal("text grant permitted document metadata", err)
	}
	grant.Documents = true
	if err := grant.CheckMessage(candidate, grant.Author, now); err != nil {
		t.Fatal("document permission rejected", err)
	}
	if err := grant.CheckRead(18, now); !errors.Is(err, ErrDenied) {
		t.Fatal("document permission expanded read prefix", err)
	}
	candidate.Message.Author = peer(t, model.PeerKindUser, 23)
	if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
		t.Fatal("document permission expanded authors", err)
	}
	candidate.Message.Author = grant.Author
	candidate.Protected = true
	if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
		t.Fatal("document permission allowed protected content", err)
	}
}

func TestDocumentPermissionPersistsAndReplacementDisablesIt(t *testing.T) {
	r, _, now := fixture(t)
	ctx := context.Background()
	lease := leaseFor(t, r)
	grant := validGrant(t, *now)
	for _, allowed := range []bool{false, true, false} {
		_, before, err := lease.Binding(ctx)
		if err != nil {
			t.Fatal(err)
		}
		grant.Documents = allowed
		if err := lease.Save(ctx, grant); err != nil {
			t.Fatal(err)
		}
		got, err := lease.Grant(ctx, grant.Peer)
		if err != nil || got != grant {
			t.Fatalf("document permission round trip: %+v %v", got, err)
		}
		listed, err := lease.List(ctx)
		if err != nil || len(listed) != 1 || listed[0] != grant {
			t.Fatalf("document permission listing: %+v %v", listed, err)
		}
		_, after, err := lease.Binding(ctx)
		if err != nil || after <= before {
			t.Fatal("document permission mutation did not invalidate authority", err)
		}
	}
	if err := lease.Audit(ctx, "req_document", "open_document", "", 1, false); err != nil {
		t.Fatal("document audit failed", err)
	}
	if err := lease.Audit(ctx, "req_uncertain_document", "open_document", model.ErrorReadEffectUncertain, 0, true); err != nil {
		t.Fatal("uncertain document audit failed", err)
	}
}
