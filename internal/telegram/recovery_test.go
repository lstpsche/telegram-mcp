package telegram

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func restartReadAccount(t *testing.T, db *sql.DB, invoke gotdtelegram.InvokeFunc) *Account {
	t.Helper()
	account, err := NewAccount(Config{Environment: TestEnvironment, APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef"), TestDC: 2}, &session.StorageMemory{}, ModeRead)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.EnableReads(context.Background(), db, readTestEpoch); err != nil {
		t.Fatal(err)
	}
	account.reads.api = tg.NewClient(readMiddleware{account.reads}.Handle(invoke))
	account.reads.self.Store(1)
	return account
}

func observeRecovery(t *testing.T, account *Account) (<-chan struct{}, <-chan error, context.CancelFunc) {
	t.Helper()
	account.reads.ready.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	ready, finished := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer close(finished)
		done <- account.reads.observe(ctx, func(ctx context.Context, _ AuthorizationStatus) error {
			close(ready)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("recovery did not stop")
		}
	})
	return ready, done, cancel
}

func TestRecoveryTooLongPreservesCheckpointAcrossRestart(t *testing.T) {
	invoke := func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetDifferenceRequest:
			if q.Pts != 10 {
				t.Error("restart skipped an unrecovered gap")
			}
			return encodeReadResponse(out, &tg.UpdatesDifferenceTooLong{Pts: 99})
		default:
			return errors.New("unexpected RPC")
		}
	}
	account, db := newReadTestAccount(t, invoke)
	for range 2 {
		ready, done, _ := observeRecovery(t, account)
		if err := <-done; err == nil {
			t.Fatal("unrecoverable gap succeeded")
		}
		select {
		case <-ready:
			t.Fatal("unrecoverable gap became ready")
		default:
		}
		var pts int
		if err := db.QueryRow("SELECT pts FROM telegram_update_state").Scan(&pts); err != nil || pts != 10 {
			t.Fatalf("unaccepted checkpoint persisted: pts=%d err=%v", pts, err)
		}
		account = restartReadAccount(t, db, invoke)
	}
}

func TestRecoveryRefreshesOnlyCompleteUserHashesAcrossRestart(t *testing.T) {
	for _, kind := range []string{"full", "min", "zero"} {
		t.Run(kind, func(t *testing.T) {
			account, db := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.UpdatesGetDifferenceRequest:
					user := &tg.User{ID: 2, FirstName: "transient recovered name", Min: kind == "min"}
					user.SetAccessHash(22)
					if kind == "zero" {
						user.SetAccessHash(0)
					}
					return encodeReadResponse(out, &tg.UpdatesDifference{Users: []tg.UserClass{user}, State: tg.UpdatesState{Pts: 12, Date: 101, Seq: 1}})
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 12, Date: 101, Seq: 1})
				default:
					return errors.New("unexpected RPC")
				}
			})
			if err := account.reads.storage.SetUserAccessHash(context.Background(), 1, 2, 11); err != nil {
				t.Fatal(err)
			}
			ready, done, cancel := observeRecovery(t, account)
			select {
			case <-ready:
			case err := <-done:
				t.Fatal("recovery failed", err)
			}
			cancel()
			<-done
			restarted := restartReadAccount(t, db, func(context.Context, bin.Encoder, bin.Decoder) error { return errors.New("unexpected RPC") })
			peer, _ := model.NewPeerID(model.PeerKindUser, 2)
			input, err := restarted.reads.inputPeer(context.Background(), peer)
			if err != nil {
				t.Fatal(err)
			}
			expected := int64(11)
			if kind == "full" {
				expected = 22
			}
			if input.(*tg.InputPeerUser).AccessHash != expected {
				t.Fatal("restart reused the wrong recovered hash")
			}
		})
	}
}

func TestRecoverySlicesPersistBeforeReadinessAndRestart(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	account, db := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetDifferenceRequest:
			switch q.Pts {
			case 10:
				return encodeReadResponse(out, &tg.UpdatesDifferenceSlice{IntermediateState: tg.UpdatesState{Pts: 12, Date: 101, Seq: 1}})
			case 12:
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return encodeReadResponse(out, &tg.UpdatesDifference{State: tg.UpdatesState{Pts: 14, Date: 102, Seq: 2}})
			}
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 14, Date: 102, Seq: 2})
		}
		return errors.New("unexpected RPC")
	})
	ready, done, cancel := observeRecovery(t, account)
	select {
	case <-entered:
	case err := <-done:
		t.Fatal(err)
	}
	var pts int
	if err := db.QueryRow("SELECT pts FROM telegram_update_state").Scan(&pts); err != nil || pts != 12 || account.Ready() {
		t.Fatal("slice was not durable before continuing", err)
	}
	close(release)
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	}
	cancel()
	<-done
	restarted := restartReadAccount(t, db, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetDifferenceRequest:
			if q.Pts != 14 || q.Date != 102 {
				t.Error("restart did not use accepted checkpoint")
			}
			return encodeReadResponse(out, &tg.UpdatesDifferenceEmpty{Date: 102, Seq: 2})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 14, Date: 102, Seq: 2})
		}
		return errors.New("unexpected RPC")
	})
	ready, done, _ = observeRecovery(t, restarted)
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	}
}

func TestRecoveryPostStartupGapBlocksHistory(t *testing.T) {
	for _, outcome := range []string{"complete", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var calls, remotePts, historyCalls atomic.Int32
			remotePts.Store(10)
			account, db := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.UpdatesGetDifferenceRequest:
					if calls.Add(1) == 1 {
						return encodeReadResponse(out, &tg.UpdatesDifferenceEmpty{Date: 100, Seq: 1})
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					if outcome == "failure" {
						return encodeReadResponse(out, &tg.UpdatesDifferenceTooLong{Pts: 99})
					}
					return encodeReadResponse(out, &tg.UpdatesDifference{State: tg.UpdatesState{Pts: 12, Date: 101, Seq: 1}})
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: int(remotePts.Load()), Date: 100, Seq: 1})
				case *tg.MessagesGetHistoryRequest:
					historyCalls.Add(1)
					return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{testMessage(5)}})
				}
				return errors.New("unexpected RPC")
			})
			ready, done, cancel := observeRecovery(t, account)
			select {
			case <-ready:
			case err := <-done:
				t.Fatal(err)
			}
			remotePts.Store(12)
			if err := account.reads.Handle(context.Background(), &tg.UpdatesTooLong{}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case err := <-done:
				t.Fatal(err)
			}
			readDone := make(chan error, 1)
			ctx, stopRead := context.WithTimeout(context.Background(), 2*time.Second)
			defer stopRead()
			go func() {
				items, err := account.History(ctx, model.HistoryQuery{Peer: testSelfPeer(t), MinID: 1, MaxID: 9, Limit: 1})
				if outcome != "complete" && len(items) != 0 {
					t.Error("failed recovery released history")
				}
				readDone <- err
			}()
			select {
			case err := <-readDone:
				t.Fatalf("history completed during gap: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if historyCalls.Load() != 0 {
				t.Error("history fetched before recovery")
			}
			if outcome == "cancel" {
				cancel()
			} else {
				close(release)
			}
			err := <-readDone
			if (err == nil) != (outcome == "complete") {
				t.Fatal("unexpected history outcome", err)
			}
			cancel()
			<-done
			var pts int
			if err := db.QueryRow("SELECT pts FROM telegram_update_state").Scan(&pts); err != nil {
				t.Fatal(err)
			}
			if outcome != "complete" && pts != 10 {
				t.Fatal("failed recovery advanced checkpoint")
			}
		})
	}
}

func TestRecoveryRejectsUnsafeDifferencesBeforeMetadata(t *testing.T) {
	for _, failure := range []string{"pts", "qts", "seq", "empty_seq", "date", "no_progress", "messages", "updates", "users", "chats", "hash_write"} {
		t.Run(failure, func(t *testing.T) {
			user := &tg.User{ID: 2}
			user.SetAccessHash(22)
			full := &tg.UpdatesDifference{Users: []tg.UserClass{user}, State: tg.UpdatesState{Pts: 12, Qts: 1, Date: 101, Seq: 1}}
			var response tg.UpdatesDifferenceClass = full
			switch failure {
			case "pts":
				full.State.Pts = 9
			case "qts":
				full.State.Qts = -1
			case "seq":
				full.State.Seq = 0
			case "empty_seq":
				response = &tg.UpdatesDifferenceEmpty{Date: 101, Seq: 0}
			case "date":
				full.State.Date = 0
			case "no_progress":
				response = &tg.UpdatesDifferenceSlice{Users: full.Users, IntermediateState: tg.UpdatesState{Pts: 10, Date: 100, Seq: 1}}
			case "messages":
				for range 101 {
					full.NewMessages = append(full.NewMessages, testMessage(5))
				}
			case "updates":
				for range 101 {
					full.OtherUpdates = append(full.OtherUpdates, &tg.UpdatePtsChanged{})
				}
			case "users":
				for range 200 {
					full.Users = append(full.Users, user)
				}
			case "chats":
				for range 201 {
					full.Chats = append(full.Chats, &tg.ChatEmpty{ID: 3})
				}
			}
			var calls int
			account, db := newReadTestAccount(t, func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
				calls++
				return encodeReadResponse(out, response)
			})
			if failure == "hash_write" {
				if _, err := db.Exec("CREATE TRIGGER reject_hash BEFORE INSERT ON telegram_peer_hashes BEGIN SELECT RAISE(FAIL, 'synthetic write failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			result, err := account.reads.UpdatesGetDifference(context.Background(), &tg.UpdatesGetDifferenceRequest{Pts: 10, Date: 100})
			if err == nil || result != nil || account.Ready() {
				t.Fatal("unsafe recovery accepted")
			}
			if _, err := account.reads.UpdatesGetState(context.Background()); err == nil || calls != 1 {
				t.Fatal("failed runtime performed another RPC")
			}
			var pts, hashes int
			if err := db.QueryRow("SELECT pts FROM telegram_update_state").Scan(&pts); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT count(*) FROM telegram_peer_hashes").Scan(&hashes); err != nil {
				t.Fatal(err)
			}
			if pts != 10 || hashes != 0 {
				t.Fatal("rejected difference changed metadata")
			}
		})
	}
}

func TestRecoveryRPCBudgetBoundsSlicesAndResetsOnCompletion(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "unterminated", true: "completed_chains"}[terminal], func(t *testing.T) {
			var calls int
			account, _ := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				calls++
				q := input.(*tg.UpdatesGetDifferenceRequest)
				if q.PtsLimit != 100 || q.PtsTotalLimit != 800 || q.QtsLimit != 100 {
					t.Error("missing recovery request bounds")
				}
				if terminal {
					return encodeReadResponse(out, &tg.UpdatesDifferenceEmpty{Date: 100, Seq: 1})
				}
				return encodeReadResponse(out, &tg.UpdatesDifferenceSlice{IntermediateState: tg.UpdatesState{Pts: q.Pts + 1, Date: 100, Seq: 1}})
			})
			for i := 0; i < 9; i++ {
				result, err := account.reads.UpdatesGetDifference(context.Background(), &tg.UpdatesGetDifferenceRequest{Pts: 10 + i, Date: 100})
				if !terminal && i == 8 {
					if err == nil || result != nil || calls != 8 {
						t.Fatal("recovery exceeded eight RPCs")
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			if terminal && calls != 9 {
				t.Fatal("completed chains shared a budget")
			}
		})
	}
}

func TestRecoveryCheckpointFailureFencesLaterWrites(t *testing.T) {
	for _, failure := range []string{"state_pts", "state_qts", "state_seq", "pts", "qts", "seq", "date_seq", "date", "write"} {
		t.Run(failure, func(t *testing.T) {
			account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { return errors.New("unexpected RPC") })
			s, ctx := account.reads.storage, context.Background()
			original := updates.State{Pts: 10, Qts: 5, Date: 100, Seq: 3}
			if err := s.SetState(ctx, 1, original); err != nil {
				t.Fatal(err)
			}
			lower := original
			var err error
			switch failure {
			case "state_pts":
				lower.Pts--
				err = s.SetState(ctx, 1, lower)
			case "state_qts":
				lower.Qts--
				err = s.SetState(ctx, 1, lower)
			case "state_seq":
				lower.Seq--
				err = s.SetState(ctx, 1, lower)
			case "pts":
				err = s.SetPts(ctx, 1, 9)
			case "qts":
				err = s.SetQts(ctx, 1, 4)
			case "seq":
				err = s.SetSeq(ctx, 1, 2)
			case "date_seq":
				err = s.SetDateSeq(ctx, 1, 101, 2)
			case "date":
				err = s.SetDate(ctx, 1, 0)
			case "write":
				if _, err := db.Exec("CREATE TRIGGER reject_checkpoint BEFORE UPDATE ON telegram_update_state BEGIN SELECT RAISE(FAIL, 'synthetic checkpoint failure'); END"); err != nil {
					t.Fatal(err)
				}
				err = s.SetPts(ctx, 1, 11)
				if _, err := db.Exec("DROP TRIGGER reject_checkpoint"); err != nil {
					t.Fatal(err)
				}
			}
			if err == nil || account.Ready() {
				t.Fatal("checkpoint failure was ignored")
			}
			if laterErr := s.SetState(ctx, 1, updates.State{Pts: 20, Qts: 10, Date: 102, Seq: 5}); !errors.Is(laterErr, err) {
				t.Fatal("later write bypassed failure latch")
			}
			var persisted updates.State
			if err := db.QueryRow("SELECT pts,qts,date,seq FROM telegram_update_state").Scan(&persisted.Pts, &persisted.Qts, &persisted.Date, &persisted.Seq); err != nil {
				t.Fatal(err)
			}
			if persisted != original {
				t.Fatal("failure replaced the last accepted checkpoint")
			}
		})
	}
}

func TestRecoveryCorruptCheckpointDoesNotFetchReplacement(t *testing.T) {
	var calls atomic.Int32
	account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		calls.Add(1)
		return errors.New("unexpected RPC")
	})
	if _, err := db.Exec("UPDATE telegram_update_state SET date=0"); err != nil {
		t.Fatal(err)
	}
	ready, done, _ := observeRecovery(t, account)
	if err := <-done; err == nil {
		t.Fatal("invalid checkpoint accepted")
	}
	select {
	case <-ready:
		t.Fatal("corrupt checkpoint became ready")
	default:
	}
	if calls.Load() != 0 {
		t.Fatal("corrupt checkpoint caused replacement RPC")
	}
}
