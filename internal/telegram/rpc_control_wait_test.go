package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func TestRetainedLongRPCWaitPreservesReadAvailabilityErrors(t *testing.T) {
	for _, delay := range []time.Duration{120 * time.Second, 48 * time.Hour} {
		t.Run(delay.String(), func(t *testing.T) {
			calls := 0
			account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return errors.New("unexpected RPC") })
			repository, err := store.NewOutgoingRepository(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := account.EnableSending(repository); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			now := time.Now().UTC()
			until := now.Add(delay)
			if err := repository.RecordWait(ctx, account.environment, 1, "contacts.resolveUsername", model.PeerID{}, until, now); err != nil {
				t.Fatal(err)
			}
			controlled := rpcControl{runtime: account.reads, environment: account.environment}.Handle(gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return errors.New("unexpected RPC") }))
			request := &tg.ContactsResolveUsernameRequest{Username: "SyntheticRecipient"}
			readErr := readError(controlled.Invoke(ctx, request, &tg.ContactsResolvedPeer{}))
			if model.TextErrorCategory(readErr) != model.ErrorTelegramUnavailable {
				t.Fatalf("long retained read wait changed its availability category: %v", readErr)
			}
			if calls != 0 {
				t.Fatal("retained wait issued an RPC", calls)
			}
			outgoing := context.WithValue(ctx, outgoingContextKey{}, &outgoingCall{})
			sendErr := controlled.Invoke(outgoing, request, &tg.ContactsResolvedPeer{})
			var retry *model.SendError
			if !errors.As(sendErr, &retry) || retry.Category != model.ErrorRateLimited || !retry.RetryAt.Equal(until) {
				t.Fatalf("outgoing retry deadline changed: %v", sendErr)
			}
			if calls != 0 {
				t.Fatal("outgoing wait issued an RPC", calls)
			}
			retained, err := repository.WaitUntil(ctx, account.environment, 1, "contacts.resolveUsername", model.PeerID{}, time.Now().UTC())
			if err != nil || !retained.Equal(until) {
				t.Fatal("read failure changed retained provider wait", retained, err)
			}
		})
	}
}
