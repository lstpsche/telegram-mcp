package telegram

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	gotdtelegram "github.com/gotd/td/telegram"
	gotdauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

const readTestEpoch = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newReadTestAccount(t *testing.T, invoke gotdtelegram.InvokeFunc) (*Account, *sql.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state", "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO authorization_state(singleton,epoch,created_at,updated_at) VALUES (1,?,'2026-09-05T00:00:00Z','2026-09-05T00:00:00Z')`, readTestEpoch)
	if err != nil {
		t.Fatal(err)
	}
	account, err := NewAccount(Config{Environment: TestEnvironment, APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef"), TestDC: 2}, &session.StorageMemory{}, ModeRead)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.EnableReads(context.Background(), db, readTestEpoch); err != nil {
		t.Fatal(err)
	}
	account.reads.api = tg.NewClient(readMiddleware{account.reads}.Handle(invoke))
	account.reads.self.Store(1)
	if err := account.reads.storage.SetState(context.Background(), 1, updates.State{Pts: 10, Qts: 0, Date: 100, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	account.reads.ready.Store(true)
	return account, db
}
func encodeReadResponse(out bin.Decoder, value bin.Encoder) error {
	var buffer bin.Buffer
	if err := value.Encode(&buffer); err != nil {
		return err
	}
	return out.Decode(&buffer)
}
func testSelfPeer(t *testing.T) model.PeerID {
	t.Helper()
	id, err := model.NewPeerID(model.PeerKindSelf, 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func testMessage(id int) *tg.Message {
	return &tg.Message{ID: id, Out: true, PeerID: &tg.PeerUser{UserID: 1}, FromID: &tg.PeerUser{UserID: 1}, Date: 100, Message: "ordinary synthetic text"}
}

func TestReadNormalizationNeverCopiesUnsafeBodies(t *testing.T) {
	for name, mutate := range map[string]func(*tg.Message){
		"blockquote": func(m *tg.Message) {
			m.Entities = []tg.MessageEntityClass{&tg.MessageEntityBlockquote{Offset: 0, Length: 5}}
		},
		"protected":      func(m *tg.Message) { m.Noforwards = true },
		"expiring":       func(m *tg.Message) { m.TTLPeriod = 10 },
		"forwarded":      func(m *tg.Message) { m.SetFwdFrom(tg.MessageFwdHeader{Date: 99, Imported: true}) },
		"quoted":         func(m *tg.Message) { m.ReplyTo = &tg.MessageReplyHeader{Quote: true, QuoteText: "hostile quote"} },
		"forum":          func(m *tg.Message) { m.ReplyTo = &tg.MessageReplyHeader{ForumTopic: true} },
		"media":          func(m *tg.Message) { m.Media = &tg.MessageMediaPhoto{} },
		"bot":            func(m *tg.Message) { m.ViaBotID = 8 },
		"saved variant":  func(m *tg.Message) { m.SavedPeerID = &tg.PeerUser{UserID: 4} },
		"unknown author": func(m *tg.Message) { m.FromID = &tg.PeerUser{UserID: 99} },
	} {
		t.Run(name, func(t *testing.T) {
			m := testMessage(5)
			m.Message = "secret body must never cross normalization"
			mutate(m)
			candidate, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true})
			if err != nil {
				t.Fatal(err)
			}
			if candidate.Message.Text != "" || candidate.Message.Date != "" {
				t.Fatal("unsafe body crossed normalization")
			}
			if !candidate.Unsupported && !candidate.Protected && !candidate.Ephemeral && !candidate.Forwarded && !candidate.Quoted {
				t.Fatal("missing safety evidence")
			}
		})
	}
	safe, err := normalizeMessage(testSelfPeer(t), 1, testMessage(5), map[int64]bool{1: true})
	if err != nil || safe.Message.Text != "ordinary synthetic text" {
		t.Fatalf("safe normalization: %v", err)
	}
	wrong := testMessage(5)
	wrong.PeerID = &tg.PeerUser{UserID: 9}
	if _, err := normalizeMessage(testSelfPeer(t), 1, wrong, map[int64]bool{1: true}); err == nil {
		t.Fatal("cross-dialog message accepted")
	}
}

func TestReadMetadataHashesAreEpochBoundAndNeverInvented(t *testing.T) {
	account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("unexpected RPC"); return nil })
	peer, _ := model.NewPeerID(model.PeerKindUser, 2)
	if _, err := account.reads.inputPeer(context.Background(), peer); err == nil {
		t.Fatal("unknown hash accepted")
	}
	minimal := &tg.User{ID: 2, Min: true}
	minimal.SetAccessHash(999)
	if err := account.reads.saveUsers(context.Background(), []tg.UserClass{minimal}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := account.reads.storage.GetUserAccessHash(context.Background(), 1, 2); err != nil || found {
		t.Fatalf("min metadata persisted: found=%v err=%v", found, err)
	}
	full := &tg.User{ID: 2, FirstName: "title must not persist"}
	full.SetAccessHash(7654321)
	if err := account.reads.saveUsers(context.Background(), []tg.UserClass{full}); err != nil {
		t.Fatal(err)
	}
	input, err := account.reads.inputPeer(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if input.(*tg.InputPeerUser).AccessHash != 7654321 {
		t.Fatal("wrong hash")
	}
	if _, err := db.Exec(`UPDATE authorization_state SET epoch=? WHERE singleton=1`, strings.Repeat("b", 43)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM telegram_peer_hashes`).Scan(&count); err != nil || count != 0 {
		t.Fatal("epoch rotation retained hashes")
	}
	if _, err := account.reads.inputPeer(context.Background(), peer); err == nil {
		t.Fatal("old epoch remained usable")
	}
	if account.Ready() {
		t.Fatal("stale epoch remained ready")
	}
}

func TestContextFetchesVerifiedTargetBeforeBoundedNeighbors(t *testing.T) {
	var historyCalls atomic.Int32
	account, _ := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesGetHistoryRequest:
			if q.AddOffset == -1 {
				if q.OffsetID != 10 || q.MinID != 9 || q.MaxID != 11 || q.Limit != 1 {
					t.Error("target lookup is not exact and peer scoped")
				}
				return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{testMessage(10)}})
			}
			historyCalls.Add(1)
			if q.MinID != 0 || q.MaxID != 21 || q.OffsetID != 10 {
				t.Errorf("incorrect query bounds")
			}
			switch q.AddOffset {
			case 0:
				if q.Limit != 2 {
					t.Error("before limit")
				}
				return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{testMessage(9), testMessage(7)}})
			case -3:
				if q.Limit != 3 {
					t.Error("after limit")
				}
				return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{testMessage(13), testMessage(11), testMessage(10)}})
			default:
				t.Error("unexpected offset")
			}
		default:
			t.Errorf("unexpected method %T", input)
		}
		return errors.New("unexpected method")
	})
	result, err := account.History(context.Background(), model.HistoryQuery{Peer: testSelfPeer(t), Target: 10, BeforeCount: 2, AfterCount: 2, MinID: 1, MaxID: 20, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := []int32{13, 11, 10, 9, 7}
	if len(result) != len(want) {
		t.Fatalf("got %d items", len(result))
	}
	for i, item := range result {
		if item.Message.ID.TelegramID() != want[i] {
			t.Fatal("wrong neighbors")
		}
	}
	if historyCalls.Load() != 2 {
		t.Fatal("incorrect neighbor RPC count")
	}
}

func TestMissingContextTargetDoesNotFetchNeighbors(t *testing.T) {
	account, _ := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		switch input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesGetHistoryRequest:
			q := input.(*tg.MessagesGetHistoryRequest)
			if q.AddOffset != -1 || q.Limit != 1 {
				t.Error("missing target fetched neighbors")
			}
			return encodeReadResponse(out, &tg.MessagesMessages{})
		default:
			t.Error("missing target fetched neighbors")
			return errors.New("unexpected method")
		}
	})
	result, err := account.History(context.Background(), model.HistoryQuery{Peer: testSelfPeer(t), Target: 10, BeforeCount: 1, AfterCount: 1, MinID: 1, MaxID: 20, Limit: 3})
	if err == nil || result != nil {
		t.Fatal("missing target returned a result")
	}
}

func TestReadAcknowledgmentWaitsForDurableHookedCheckpoint(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "checkpoint rejected"}[failWrite], func(t *testing.T) {
			var serverPts atomic.Int32
			serverPts.Store(10)
			var receipts atomic.Int32
			account, db := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: int(serverPts.Load()), Date: 100, Seq: 1})
				case *tg.UpdatesGetDifferenceRequest:
					return encodeReadResponse(out, &tg.UpdatesDifferenceEmpty{Date: 100, Seq: 1})
				case *tg.MessagesReadHistoryRequest:
					receipts.Add(1)
					serverPts.Store(11)
					return encodeReadResponse(out, &tg.MessagesAffectedMessages{Pts: 11, PtsCount: 1})
				default:
					return errors.New("unexpected method")
				}
			})
			if failWrite {
				if _, err := db.Exec(`CREATE TRIGGER reject_receipt BEFORE UPDATE OF pts ON telegram_update_state WHEN NEW.pts=11 BEGIN SELECT RAISE(FAIL,'sensitive diagnostic'); END`); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- account.reads.manager.Run(ctx, account.reads, 1, updates.AuthOptions{OnStart: func(context.Context) { close(started) }})
			}()
			<-started
			err := account.Acknowledge(ctx, testSelfPeer(t), 20)
			if failWrite {
				if err == nil || strings.Contains(err.Error(), "sensitive") {
					t.Fatalf("unsafe failure: %v", err)
				}
				if account.Ready() {
					t.Error("failed checkpoint left account ready")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var pts int
				if err := db.QueryRow(`SELECT pts FROM telegram_update_state`).Scan(&pts); err != nil || pts != 11 {
					t.Fatalf("receipt returned before checkpoint: pts=%d err=%v", pts, err)
				}
			}
			if receipts.Load() != 1 {
				t.Fatal("receipt was not attempted exactly once")
			}
			cancel()
			<-done
		})
	}
}

func TestReadAcknowledgmentCannotSucceedWithoutRunningManager(t *testing.T) {
	account, _ := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		switch input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesReadHistoryRequest:
			return encodeReadResponse(out, &tg.MessagesAffectedMessages{Pts: 11, PtsCount: 1})
		default:
			return errors.New("unexpected method")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := account.Acknowledge(ctx, testSelfPeer(t), 20); err == nil {
		t.Fatal("enqueue without durable checkpoint succeeded")
	}
}

func TestHistoryStartsInsideGrantedRange(t *testing.T) {
	account, _ := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesGetHistoryRequest:
			if q.OffsetID != 21 || q.MinID != 9 || q.MaxID != 21 || q.Limit != 2 {
				t.Error("history did not anchor at inclusive grant maximum")
			}
			return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{testMessage(20), testMessage(18)}})
		default:
			return errors.New("unexpected method")
		}
	})
	result, err := account.History(context.Background(), model.HistoryQuery{Peer: testSelfPeer(t), MinID: 10, MaxID: 20, Limit: 2})
	if err != nil || len(result) != 2 {
		t.Fatalf("history returned %d records, err=%v", len(result), err)
	}
}

func TestReadRecoveryFailureKeepsBodiesUnavailable(t *testing.T) {
	account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { return errors.New("hostile RPC text") })
	_, err := account.reads.UpdatesGetDifference(context.Background(), &tg.UpdatesGetDifferenceRequest{Pts: 10})
	if err == nil || account.Ready() {
		t.Fatal("failed recovery did not disable reads")
	}
	_, err = account.History(context.Background(), model.HistoryQuery{Peer: testSelfPeer(t), MinID: 1, MaxID: 20, Limit: 2})
	if err == nil || strings.Contains(err.Error(), "hostile") {
		t.Fatal("degraded read leaked RPC cause")
	}
}

func TestReadStartupWaitsForInitialDifferenceProcessing(t *testing.T) {
	for _, failRecovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "failed"}[failRecovery], func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			account, _ := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.UpdatesGetDifferenceRequest:
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					if failRecovery {
						return errors.New("initial recovery failed")
					}
					return encodeReadResponse(out, &tg.UpdatesDifferenceEmpty{Date: 100, Seq: 1})
				default:
					return errors.New("unexpected method")
				}
			})
			account.reads.ready.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := make(chan struct{})
			managerDone := make(chan error, 1)
			go func() {
				managerDone <- account.reads.manager.Run(ctx, account.reads, 1, updates.AuthOptions{OnStart: func(context.Context) { close(started) }})
			}()
			<-started
			<-entered
			readyDone := make(chan error, 1)
			go func() { readyDone <- account.reads.awaitStartup(ctx) }()
			select {
			case err := <-readyDone:
				t.Fatalf("startup completed before recovery: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if account.Ready() {
				t.Error("blocked recovery made account ready")
			}
			close(release)
			err := <-readyDone
			if failRecovery && err == nil {
				t.Error("failed initial recovery passed startup barrier")
			}
			if !failRecovery && err != nil {
				t.Fatalf("completed recovery: %v", err)
			}
			cancel()
			<-managerDone
		})
	}
}

func TestReadStateRejectsInvalidRemoteSequence(t *testing.T) {
	for name, state := range map[string]tg.UpdatesState{
		"pts": {Pts: -1, Date: 100, Seq: 1}, "qts": {Pts: 10, Qts: -1, Date: 100, Seq: 1},
		"seq": {Pts: 10, Date: 100, Seq: -1}, "date": {Pts: 10, Date: 0, Seq: 1},
	} {
		t.Run(name, func(t *testing.T) {
			account, _ := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
				return encodeReadResponse(out, &state)
			})
			if err := account.reads.synchronize(context.Background()); err == nil {
				t.Fatal("invalid remote state passed checkpoint comparison")
			}
			if account.Ready() {
				t.Fatal("invalid remote state left account ready")
			}
		})
	}
}

func TestReadMiddlewareRechecksFailureAfterWaiting(t *testing.T) {
	var rpcCalls atomic.Int32
	account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { rpcCalls.Add(1); return nil })
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	// Model the outer rate/flood middleware: the read middleware is the final
	// boundary after its wait, immediately before the transport invoker.
	go func() {
		close(entered)
		<-release
		_, err := account.reads.api.MessagesReadHistory(context.Background(), &tg.MessagesReadHistoryRequest{Peer: &tg.InputPeerSelf{}, MaxID: 20})
		done <- err
	}()
	<-entered
	account.reads.fail(errors.New("asynchronous checkpoint failure"))
	close(release)
	if err := <-done; err == nil {
		t.Fatal("latched failure was ignored")
	}
	if rpcCalls.Load() != 0 {
		t.Fatal("receipt RPC ran after asynchronous failure")
	}
}

func TestReadMetadataRejectsZeroAccessHashes(t *testing.T) {
	account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("unexpected RPC"); return nil })
	zero := &tg.User{ID: 2}
	zero.SetAccessHash(0)
	if err := account.reads.saveUsers(context.Background(), []tg.UserClass{zero}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := account.reads.storage.GetUserAccessHash(context.Background(), 1, 2); err != nil || found {
		t.Fatal("zero hash was persisted")
	}
	peer, _ := model.NewPeerID(model.PeerKindUser, 2)
	if _, err := account.reads.inputPeer(context.Background(), peer); err == nil {
		t.Fatal("zero hash resolved an input peer")
	}
	if err := account.reads.storage.SetUserAccessHash(context.Background(), 1, 2, 0); err == nil {
		t.Fatal("storage accepted a zero hash")
	}
}

func TestAuthorizationLossStopsReadObservation(t *testing.T) {
	account, _ := newReadTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		switch input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.UpdatesGetDifferenceRequest:
			return encodeReadResponse(out, &tg.UpdatesDifferenceEmpty{Date: 100, Seq: 1})
		case *tg.UsersGetUsersRequest:
			return tgerr.New(401, "SESSION_EXPIRED")
		default:
			return errors.New("unexpected method")
		}
	})
	if err := account.reads.storage.SetUserAccessHash(context.Background(), 1, 2, 123); err != nil {
		t.Fatal(err)
	}
	account.reads.ready.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- account.reads.observe(ctx, func(ctx context.Context, status AuthorizationStatus) error {
			close(ready)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-ready
	peer, _ := model.NewPeerID(model.PeerKindUser, 2)
	if _, err := account.Chat(ctx, peer); !gotdauth.IsUnauthorized(err) {
		t.Fatalf("chat error lost authorization cause: %v", err)
	}
	select {
	case err := <-done:
		if !gotdauth.IsUnauthorized(err) {
			t.Fatalf("observation lost authorization failure: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("read observation did not exit after authorization loss")
	}
	if account.Ready() {
		t.Fatal("lost authorization left account ready")
	}
}

func TestAuthorizationFailureTakesPrecedenceOverUpdateLogFailure(t *testing.T) {
	account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { return nil })
	account.reads.fail(errors.New("update processing failed"))
	account.reads.fail(tgerr.New(401, "SESSION_EXPIRED"))
	if !gotdauth.IsUnauthorized(account.reads.err()) {
		t.Fatal("generic update error hid authorization loss")
	}
}

func TestOwnSavedDialogMessagesNormalizeWithoutForwardedAuthority(t *testing.T) {
	for _, message := range []*tg.Message{testMessage(10), testPhotoMessage(), testDocumentMessage()} {
		message.SavedPeerID = &tg.PeerUser{UserID: 1}
		message.FromID = nil
		if message.Media != nil {
			message.Message = ""
		}
		candidate, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true})
		if err != nil || candidate.Unsupported || candidate.Forwarded || candidate.Message.Author.String() != "tgpeer:v1:user:1" || candidate.Message.Date == "" {
			t.Fatal("own saved message rejected", err)
		}
		if message.Media != nil && candidate.Image == nil {
			t.Fatal("captionless saved image lost descriptor")
		}
		message.Flags.Set(2)
		candidate, err = normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true})
		if err != nil || !candidate.Forwarded || candidate.Image != nil || candidate.Message.Text != "" {
			t.Fatal("saved dialog bypassed forwarded-content exclusion", err)
		}
	}
}

func TestSavedDialogMetadataCannotAuthorizeOtherOrigins(t *testing.T) {
	for _, saved := range []tg.PeerClass{&tg.PeerUser{UserID: 2}, &tg.PeerChat{ChatID: 1}, &tg.PeerChannel{ChannelID: 1}} {
		message := testPhotoMessage()
		message.SavedPeerID = saved
		candidate, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true})
		if err != nil || !candidate.Unsupported || candidate.Image != nil || candidate.Message.Text != "" {
			t.Fatal("other saved origin released content", err)
		}
	}
	for _, kind := range []model.PeerKind{model.PeerKindSelf, model.PeerKindUser, model.PeerKindChat} {
		peer, _ := model.NewPeerID(kind, 2)
		message := testPhotoMessage()
		message.PeerID = &tg.PeerUser{UserID: 2}
		if kind == model.PeerKindChat {
			message.PeerID = &tg.PeerChat{ChatID: 2}
		}
		message.SavedPeerID = message.PeerID
		candidate, err := normalizeMessage(peer, 1, message, map[int64]bool{1: true})
		if err != nil || !candidate.Unsupported || candidate.Image != nil || candidate.Message.Text != "" {
			t.Fatal("non-self dialog released saved content", err)
		}
	}
}
