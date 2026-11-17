package telegram

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func TestRecipientUsernameRequiresMatchingUsableIdentity(t *testing.T) {
	for _, variant := range []string{"primary", "active alias", "inactive alias", "wrong peer", "minimal", "deleted", "missing hash", "extra identity"} {
		t.Run(variant, func(t *testing.T) {
			user := &tg.User{ID: 2, FirstName: "Synthetic recipient", Bot: true}
			user.SetAccessHash(7654321)
			user.SetUsername("recipient")
			response := &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 2}, Users: []tg.UserClass{user}}
			switch variant {
			case "active alias", "inactive alias":
				user.SetUsername("primary")
				user.SetUsernames([]tg.Username{{Username: "recipient", Active: variant == "active alias"}})
			case "wrong peer":
				response.Peer = &tg.PeerUser{UserID: 3}
			case "minimal":
				user.Min = true
			case "deleted":
				user.Deleted = true
			case "missing hash":
				user.SetAccessHash(0)
			case "extra identity":
				other := &tg.User{ID: 3}
				other.SetAccessHash(999)
				response.Users = append(response.Users, other)
			}
			calls := 0
			account, db := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch query := input.(type) {
				case *tg.ContactsResolveUsernameRequest:
					calls++
					if query.Username != "recipient" {
						t.Errorf("unexpected lookup spelling %q", query.Username)
					}
					return encodeReadResponse(out, response)
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				default:
					t.Fatalf("unexpected recipient RPC %T", input)
					return errors.New("unexpected method")
				}
			})
			repository, err := store.NewOutgoingRepository(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := account.EnableSending(repository); err != nil {
				t.Fatal(err)
			}
			resolved, err := account.ResolveRecipient(context.Background(), model.ResolveRecipientQuery{Username: "@recipient"})
			valid := variant == "primary" || variant == "active alias"
			if valid {
				peer, _ := model.NewPeerID(model.PeerKindUser, 2)
				if err != nil || resolved.Peer != peer || resolved.Kind != model.PeerKindUser {
					t.Fatalf("valid identity rejected: result=%+v err=%v", resolved, err)
				}
				if hash, found, err := account.reads.storage.GetUserAccessHash(context.Background(), 1, 2); err != nil || !found || hash != 7654321 {
					t.Fatalf("selected hash missing: found=%v hash=%d err=%v", found, hash, err)
				}
			} else if err == nil {
				t.Fatal("unverified recipient identity accepted")
			}
			if calls != 1 {
				t.Fatalf("lookup count %d", calls)
			}
			var hashes int
			if err := db.QueryRow("SELECT count(*) FROM telegram_peer_hashes").Scan(&hashes); err != nil {
				t.Fatal(err)
			}
			if valid && hashes != 1 || !valid && hashes != 0 {
				t.Fatalf("unselected metadata retained: %d hashes", hashes)
			}
		})
	}
}

func TestRecipientPhoneLookupReservesOneSharedSlot(t *testing.T) {
	calls := 0
	account, _, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch query := input.(type) {
		case *tg.ContactsResolvePhoneRequest:
			calls++
			if query.Phone != "+15551234567" && query.Phone != "15551234567" {
				t.Errorf("phone changed: %q", query.Phone)
			}
			user := &tg.User{ID: 2, FirstName: "Synthetic recipient"}
			user.SetAccessHash(7654321)
			user.SetPhone("15551234567")
			return encodeReadResponse(out, &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 2}, Users: []tg.UserClass{user}})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		default:
			t.Fatalf("unexpected phone lookup RPC %T", input)
			return errors.New("unexpected method")
		}
	})
	if _, err := account.ResolveRecipient(context.Background(), model.ResolveRecipientQuery{Phone: "+15551234567"}); err != nil {
		t.Fatal(err)
	}
	if _, err := account.ResolveRecipient(context.Background(), model.ResolveRecipientQuery{Phone: "+15551234567"}); err == nil {
		t.Fatal("second phone lookup bypassed shared interval")
	}
	if calls != 1 {
		t.Fatalf("phone lookup calls=%d; second call must be gated", calls)
	}
}

func TestRecipientKnownIDNeverInventsAccessMetadata(t *testing.T) {
	account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("unknown peer initiated Telegram lookup")
		return nil
	})
	repository, err := store.NewOutgoingRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.EnableSending(repository); err != nil {
		t.Fatal(err)
	}
	peer, _ := model.NewPeerID(model.PeerKindUser, 999)
	if _, err := account.ResolveRecipient(context.Background(), model.ResolveRecipientQuery{Peer: peer}); err == nil {
		t.Fatal("unknown access hash accepted")
	}
}

func TestRecipientPhoneReservesIntervalAfterOuterLimiter(t *testing.T) {
	calls := 0
	invoke := gotdtelegram.InvokeFunc(func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		if _, ok := input.(*tg.ContactsResolvePhoneRequest); !ok {
			t.Fatalf("unexpected phone RPC %T", input)
		}
		calls++
		user := &tg.User{ID: 2, FirstName: "Synthetic recipient"}
		user.SetAccessHash(7654321)
		user.SetPhone("15551234567")
		return encodeReadResponse(out, &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 2}, Users: []tg.UserClass{user}})
	})
	account, _, db := newSendingTestAccount(t, invoke)
	rateIndex, controlIndex := -1, -1
	for index, middleware := range account.middlewares {
		switch middleware.(type) {
		case *ratelimit.RateLimiter:
			rateIndex = index
		case rpcControl:
			controlIndex = index
		}
	}
	if rateIndex < 0 || controlIndex <= rateIndex {
		t.Fatal("phone reservation gate must follow the aggregate rate limiter")
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce, blockOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	controlled := rpcControl{runtime: account.reads, environment: account.environment}.Handle(readMiddleware{account.reads}.Handle(invoke))
	account.reads.api = tg.NewClient(gotdtelegram.InvokeFunc(func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		blockOnce.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		if err := ctx.Err(); err != nil {
			return err
		}
		return controlled.Invoke(ctx, input, out)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := account.ResolveRecipient(ctx, model.ResolveRecipientQuery{Phone: "+15551234567"})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("phone request did not reach the outer limiter")
	}
	var reservations int
	if err := db.QueryRow("SELECT count(*) FROM telegram_rpc_waits").Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("phone interval was consumed while waiting outside the gate: rows=%d err=%v", reservations, err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := account.ResolveRecipient(ctx, model.ResolveRecipientQuery{Phone: "+15551234567"}); err == nil {
		t.Fatal("immediate second phone lookup bypassed the reserved interval")
	}
	if calls != 1 {
		t.Fatalf("phone RPC calls=%d", calls)
	}
}
