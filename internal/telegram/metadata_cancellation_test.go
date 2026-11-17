package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func cancelBlockedMetadataCall(t *testing.T, database *sql.DB, call func(context.Context) error) error {
	t.Helper()
	held, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	before := database.Stats().WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- call(ctx) }()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for database.Stats().WaitCount == before {
		select {
		case err := <-done:
			t.Fatalf("metadata call did not wait for the held connection: %v", err)
		case <-tick.C:
		case <-timeout.C:
			t.Fatal("metadata call did not request the database connection")
		}
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-timeout.C:
		t.Fatal("metadata call did not return after cancellation")
		return nil
	}
}

func TestMetadataConnectionWaitCancellationLeavesRuntimeAvailable(t *testing.T) {
	for _, operation := range []string{"RPC wait", "phone RPC wait", "phone reservation", "send preparation", "epoch lookup", "checkpoint lookup", "hash lookup", "hash write", "channel enumeration"} {
		t.Run(operation, func(t *testing.T) {
			var rpcCalls atomic.Int32
			invoke := gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				rpcCalls.Add(1)
				return nil
			})
			account, repository, database := newSendingTestAccount(t, invoke)
			if err := account.reads.storage.SetUserAccessHash(context.Background(), 1, 2, 13); err != nil {
				t.Fatal(err)
			}
			if err := account.reads.storage.SetChannelPts(context.Background(), 1, 7, 11); err != nil {
				t.Fatal(err)
			}
			control := (rpcControl{runtime: account.reads, environment: account.environment}).Handle(invoke)
			var call func(context.Context) error
			switch operation {
			case "RPC wait", "phone RPC wait":
				call = func(ctx context.Context) error {
					ctx = context.WithValue(ctx, outgoingContextKey{}, &outgoingCall{})
					var input bin.Encoder = &tg.ContactsResolveUsernameRequest{Username: "synthetic"}
					if operation == "phone RPC wait" {
						input = &tg.ContactsResolvePhoneRequest{Phone: "+15551234567"}
					}
					return control.Invoke(ctx, input, &tg.ContactsResolvedPeer{})
				}
			case "phone reservation":
				call = func(ctx context.Context) error {
					_, acquired, err := repository.ReservePhoneLookup(ctx, account.environment, 1, time.Now().UTC())
					if err == nil && !acquired {
						return errors.New("canceled lookup consumed the phone reservation")
					}
					return account.reads.metadataError(err)
				}
			case "send preparation":
				id, err := model.NewOperationID(time.Now().UTC(), bytes.NewReader(make([]byte, 32)))
				if err != nil {
					t.Fatal(err)
				}
				call = func(ctx context.Context) error {
					prepared, err := account.PrepareSend(ctx, model.SendAttempt{OperationID: id, Peer: testSelfPeer(t), Text: "Synthetic text", RandomID: 3})
					if err == nil && prepared == nil {
						return errors.New("preparation returned no sending action")
					}
					return err
				}
			case "epoch lookup":
				call = account.reads.storage.checkEpoch
			case "checkpoint lookup":
				call = func(ctx context.Context) error {
					state, found, err := account.reads.storage.GetState(ctx, 1)
					if err == nil && (!found || state.Pts != 10) {
						return errors.New("checkpoint lookup returned a substitute")
					}
					return err
				}
			case "hash lookup":
				call = func(ctx context.Context) error {
					hash, found, err := account.reads.storage.GetUserAccessHash(ctx, 1, 2)
					if err == nil && (!found || hash != 13) {
						return errors.New("hash lookup returned a substitute")
					}
					return err
				}
			case "hash write":
				call = func(ctx context.Context) error { return account.reads.storage.SetUserAccessHash(ctx, 1, 2, 19) }
			case "channel enumeration":
				call = func(ctx context.Context) error {
					return account.reads.storage.ForEachChannels(ctx, 1, func(context.Context, int64, int) error { return nil })
				}
			}
			err := cancelBlockedMetadataCall(t, database, call)
			if !errors.Is(err, context.Canceled) || !account.Ready() || account.reads.err() != nil || rpcCalls.Load() != 0 {
				t.Fatalf("request cancellation invalidated account or initiated an RPC: ready=%v RPCs=%d err=%v", account.Ready(), rpcCalls.Load(), err)
			}
			select {
			case <-account.reads.failed:
				t.Fatal("request cancellation stopped the account runtime")
			default:
			}
			if operation == "hash write" {
				hash, found, err := account.reads.storage.GetUserAccessHash(context.Background(), 1, 2)
				if err != nil || !found || hash != 13 {
					t.Fatal("canceled metadata write changed the stored hash")
				}
			}
			if err := call(context.Background()); err != nil || !account.Ready() {
				t.Fatalf("later call did not recover locally: ready=%v err=%v", account.Ready(), err)
			}
			if operation == "hash write" {
				hash, found, err := account.reads.storage.GetUserAccessHash(context.Background(), 1, 2)
				if err != nil || !found || hash != 19 {
					t.Fatal("later metadata write was skipped")
				}
			}
			expectedCalls := int32(0)
			if operation == "RPC wait" || operation == "phone RPC wait" {
				expectedCalls = 1
			}
			if rpcCalls.Load() != expectedCalls {
				t.Fatalf("unexpected RPCs after retry: %d", rpcCalls.Load())
			}
		})
	}
}

func TestMetadataCancellationDoesNotHideCorruptionOrFailedCheckpointWrites(t *testing.T) {
	for _, failure := range []string{"missing epoch", "malformed wait", "checkpoint write", "joined corruption"} {
		t.Run(failure, func(t *testing.T) {
			account, _, database := newSendingTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
				t.Fatal("invalid metadata must not initiate an RPC")
				return nil
			})
			var err error
			switch failure {
			case "missing epoch":
				if _, err := database.Exec(`DELETE FROM authorization_state`); err != nil {
					t.Fatal(err)
				}
				err = account.reads.storage.checkEpoch(context.Background())
			case "malformed wait":
				if _, err := database.Exec(`INSERT INTO telegram_rpc_waits(environment,account_id,method,scope_peer,not_before) VALUES ('test:2',1,'contacts.resolveUsername','','invalid')`); err != nil {
					t.Fatal(err)
				}
				err = (rpcControl{runtime: account.reads, environment: account.environment}).Handle(gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return nil })).Invoke(context.Background(), &tg.ContactsResolveUsernameRequest{}, &tg.ContactsResolvedPeer{})
			case "checkpoint write":
				if _, err := database.Exec(`CREATE TRIGGER reject_checkpoint BEFORE UPDATE ON telegram_update_state BEGIN SELECT RAISE(FAIL,'private checkpoint failure'); END`); err != nil {
					t.Fatal(err)
				}
				err = account.reads.storage.SetPts(context.Background(), 1, 11)
			case "joined corruption":
				cause := errors.New("private metadata corruption")
				err = account.reads.metadataError(fmt.Errorf("metadata operation: %w", errors.Join(context.Canceled, cause)))
				if !errors.Is(err, cause) || strings.Contains(err.Error(), "private metadata") {
					t.Fatal("metadata cause was lost or exposed")
				}
			}
			if err == nil || account.Ready() || account.reads.err() == nil {
				t.Fatalf("metadata failure did not fail closed: ready=%v err=%v", account.Ready(), err)
			}
		})
	}
}

func TestCheckpointPrecheckCancellationStillStopsRuntime(t *testing.T) {
	for _, checkpoint := range []string{"pts", "date and sequence"} {
		t.Run(checkpoint, func(t *testing.T) {
			account, database := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
				t.Fatal("checkpoint storage cannot initiate an RPC")
				return nil
			})
			write := func(ctx context.Context) error {
				if checkpoint == "pts" {
					return account.reads.storage.SetPts(ctx, 1, 11)
				}
				return account.reads.storage.SetDateSeq(ctx, 1, 101, 2)
			}
			err := cancelBlockedMetadataCall(t, database, write)
			if !errors.Is(err, context.Canceled) || account.Ready() || !errors.Is(account.reads.err(), context.Canceled) {
				t.Fatalf("failed checkpoint setter left the update runtime available: ready=%v err=%v", account.Ready(), err)
			}
			var pts, date, seq int
			if err := database.QueryRow(`SELECT pts,date,seq FROM telegram_update_state WHERE epoch=? AND user_id=1`, readTestEpoch).Scan(&pts, &date, &seq); err != nil {
				t.Fatal(err)
			}
			if pts != 10 || date != 100 || seq != 1 {
				t.Fatalf("canceled checkpoint write changed durable state: pts=%d date=%d seq=%d", pts, date, seq)
			}
		})
	}
}
