package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func outgoingFixture(t *testing.T) (*OutgoingRepository, *sql.DB, string, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "metadata.db")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repository, err := NewOutgoingRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	if _, err := database.Exec(`INSERT INTO authorization_state(singleton,epoch,created_at,updated_at) VALUES (1,?,?,?)`, strings.Repeat("a", 43), outgoingTimestamp(now), outgoingTimestamp(now)); err != nil {
		t.Fatal(err)
	}
	return repository, database, path, now
}

func outgoingCandidate(t *testing.T, at time.Time, randomID int64) OutgoingOperation {
	t.Helper()
	id, err := model.NewOperationID(at, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := model.NewPeerID(model.PeerKindUser, 55)
	if err != nil {
		t.Fatal(err)
	}
	return OutgoingOperation{OperationID: id, AuthorizationEpoch: strings.Repeat("a", 43), Peer: peer, RandomID: randomID, PayloadDigest: [32]byte{1}}
}

func requireAdmit(t *testing.T, repository *OutgoingRepository, operation OutgoingOperation, now time.Time) OutgoingOperation {
	t.Helper()
	admitted, created, err := repository.Admit(context.Background(), operation, now)
	if err != nil || !created {
		t.Fatalf("Admit() created=%v error=%v", created, err)
	}
	return admitted
}

func TestOutgoingAdmissionBindsPayloadEpochAndRandomID(t *testing.T) {
	repository, database, _, now := outgoingFixture(t)
	ctx := context.Background()
	candidate := outgoingCandidate(t, now, 123)
	admitted := requireAdmit(t, repository, candidate, now)
	if admitted.State != model.SendNotSent || !admitted.CreatedAt.Equal(now) || !admitted.RetainUntil.Equal(now.Add(OutgoingRetention)) {
		t.Fatalf("unexpected admitted metadata: %#v", admitted)
	}
	candidate.RandomID = 456
	duplicate, created, err := repository.Admit(ctx, candidate, now.Add(time.Second))
	if err != nil || created || duplicate.RandomID != 123 {
		t.Fatalf("duplicate admission created=%v random=%d error=%v", created, duplicate.RandomID, err)
	}
	for _, change := range []func(*OutgoingOperation){
		func(o *OutgoingOperation) { o.AuthorizationEpoch = strings.Repeat("b", 43) },
		func(o *OutgoingOperation) { o.PayloadDigest[0]++ },
		func(o *OutgoingOperation) { o.Peer, _ = model.NewPeerID(model.PeerKindUser, 56) },
		func(o *OutgoingOperation) { id, _ := model.NewMessageID(o.Peer, 10); o.ReplyTo = &id },
	} {
		changed := candidate
		change(&changed)
		if _, _, err := repository.Admit(ctx, changed, now); !errors.Is(err, ErrOutgoingConflict) {
			t.Fatalf("changed payload admission error=%v", err)
		}
	}
	second := outgoingCandidate(t, now.Add(time.Millisecond), 123)
	if _, _, err := repository.Admit(ctx, second, now); !errors.Is(err, ErrOutgoingConflict) {
		t.Fatalf("reused random ID error=%v", err)
	}
	var count int
	if err := database.QueryRow("SELECT count(*) FROM outgoing_operations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("retained rows=%d error=%v", count, err)
	}
}

func TestOutgoingDispatchAndReceiptsAreMonotonic(t *testing.T) {
	repository, _, _, now := outgoingFixture(t)
	operation := requireAdmit(t, repository, outgoingCandidate(t, now, 123), now)
	ctx := context.Background()
	var group sync.WaitGroup
	dispatches := make(chan bool, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, dispatch, err := repository.BeginDispatch(ctx, operation.OperationID, operation.AuthorizationEpoch, now)
			if err != nil {
				t.Errorf("BeginDispatch() error=%v", err)
			}
			dispatches <- dispatch
		}()
	}
	group.Wait()
	close(dispatches)
	var count int
	for dispatch := range dispatches {
		if dispatch {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("dispatch barriers acquired=%d", count)
	}
	operation, err := repository.RecordUncertain(ctx, operation.OperationID, operation.AuthorizationEpoch, model.SendFailureEffectUncertain, nil, now)
	if err != nil || operation.State != model.SendUncertain {
		t.Fatalf("RecordUncertain() state=%v error=%v", operation.State, err)
	}
	operation, err = repository.RecordNotSent(ctx, operation.OperationID, operation.AuthorizationEpoch, model.SendFailureWriteForbidden, nil, now)
	if err != nil || operation.State != model.SendUncertain {
		t.Fatalf("uncertainty downgraded state=%v error=%v", operation.State, err)
	}
	if _, matched, err := repository.RecordReceipt(ctx, strings.Repeat("b", 43), 123, 77, nil, now); err != nil || matched {
		t.Fatalf("wrong epoch receipt matched=%v error=%v", matched, err)
	}
	if _, matched, err := repository.RecordReceipt(ctx, operation.AuthorizationEpoch, 999, 77, nil, now); err != nil || matched {
		t.Fatalf("unrelated receipt matched=%v error=%v", matched, err)
	}
	operation, matched, err := repository.RecordReceipt(ctx, operation.AuthorizationEpoch, 123, 77, nil, now)
	if err != nil || !matched || operation.State != model.SendSent || operation.MessageID == nil || operation.MessageID.TelegramID() != 77 || operation.SentAt != nil {
		t.Fatalf("positive mapping state=%v matched=%v error=%v", operation.State, matched, err)
	}
	providerTime := now.Add(-time.Second)
	operation, matched, err = repository.RecordReceipt(ctx, operation.AuthorizationEpoch, 123, 77, &providerTime, now)
	if err != nil || !matched || operation.SentAt == nil || !operation.SentAt.Equal(providerTime) {
		t.Fatalf("receipt date enrichment matched=%v error=%v", matched, err)
	}
	if _, _, err := repository.RecordReceipt(ctx, operation.AuthorizationEpoch, 123, 78, &providerTime, now); !errors.Is(err, ErrOutgoingConflict) {
		t.Fatalf("conflicting receipt error=%v", err)
	}
	otherDate := providerTime.Add(time.Second)
	if _, _, err := repository.RecordReceipt(ctx, operation.AuthorizationEpoch, 123, 77, &otherDate, now); !errors.Is(err, ErrOutgoingConflict) {
		t.Fatalf("conflicting receipt date error=%v", err)
	}
	operation, err = repository.RecordUncertain(ctx, operation.OperationID, operation.AuthorizationEpoch, model.SendFailureStateSyncFailed, nil, now)
	if err != nil || operation.State != model.SendSent || operation.MessageID.TelegramID() != 77 {
		t.Fatalf("sent receipt downgraded state=%v error=%v", operation.State, err)
	}
}

func TestOutgoingNewAdmissionRequiresCurrentEpochButReceiptSurvivesLogout(t *testing.T) {
	repository, database, _, now := outgoingFixture(t)
	ctx := context.Background()
	operation := requireAdmit(t, repository, outgoingCandidate(t, now, 123), now)
	if _, err := database.Exec("UPDATE authorization_state SET epoch=? WHERE singleton=1", strings.Repeat("b", 43)); err != nil {
		t.Fatal(err)
	}
	stale := outgoingCandidate(t, now.Add(time.Millisecond), 124)
	if _, _, err := repository.Admit(ctx, stale, now); !errors.Is(err, ErrOutgoingUnavailable) {
		t.Fatalf("stale epoch admission error=%v", err)
	}
	if _, _, err := repository.BeginDispatch(ctx, operation.OperationID, operation.AuthorizationEpoch, now); !errors.Is(err, ErrOutgoingUnavailable) {
		t.Fatalf("stale epoch dispatch error=%v", err)
	}
	if _, err := database.Exec("DELETE FROM authorization_state"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.Admit(ctx, stale, now); !errors.Is(err, ErrOutgoingUnavailable) {
		t.Fatalf("logged-out admission error=%v", err)
	}
	confirmed, matched, err := repository.RecordReceipt(ctx, operation.AuthorizationEpoch, operation.RandomID, 77, nil, now)
	if err != nil || !matched || confirmed.State != model.SendSent {
		t.Fatalf("late logged-out receipt state=%v matched=%v error=%v", confirmed.State, matched, err)
	}
}

func TestOutgoingDefinitiveRejectionRetriesSameRandomIDAfterWait(t *testing.T) {
	repository, _, _, now := outgoingFixture(t)
	ctx := context.Background()
	operation := requireAdmit(t, repository, outgoingCandidate(t, now, 123), now)
	if _, dispatch, err := repository.BeginDispatch(ctx, operation.OperationID, operation.AuthorizationEpoch, now); err != nil || !dispatch {
		t.Fatalf("BeginDispatch() dispatch=%v error=%v", dispatch, err)
	}
	retryAt := now.Add(time.Minute)
	operation, err := repository.RecordNotSent(ctx, operation.OperationID, operation.AuthorizationEpoch, model.SendFailureRateLimited, &retryAt, now)
	if err != nil || operation.State != model.SendNotSent {
		t.Fatalf("RecordNotSent() error=%v", err)
	}
	if _, _, err := repository.BeginDispatch(ctx, operation.OperationID, operation.AuthorizationEpoch, retryAt.Add(-time.Nanosecond)); !errors.Is(err, ErrOutgoingWait) {
		t.Fatalf("early retry error=%v", err)
	}
	operation, dispatch, err := repository.BeginDispatch(ctx, operation.OperationID, operation.AuthorizationEpoch, retryAt)
	if err != nil || !dispatch || operation.RandomID != 123 || operation.FailureCode != "" || operation.RetryAt != nil {
		t.Fatalf("retry dispatch=%v operation=%#v error=%v", dispatch, operation, err)
	}
}

func TestOutgoingUncertainAttemptRetainsProviderReasonAndDeadline(t *testing.T) {
	repository, _, _, now := outgoingFixture(t)
	ctx := context.Background()
	operation := requireAdmit(t, repository, outgoingCandidate(t, now, 123), now)
	operation, err := repository.RecordNotSent(ctx, operation.OperationID, operation.AuthorizationEpoch, model.SendFailureMessageTooLong, nil, now)
	if err != nil || operation.State != model.SendNotSent || operation.FailureCode != model.SendFailureMessageTooLong {
		t.Fatalf("message length rejection state=%v code=%v error=%v", operation.State, operation.FailureCode, err)
	}
	if _, dispatch, err := repository.BeginDispatch(ctx, operation.OperationID, operation.AuthorizationEpoch, now); err != nil || !dispatch {
		t.Fatalf("BeginDispatch() dispatch=%v error=%v", dispatch, err)
	}
	retryAt := now.Add(48 * time.Hour)
	operation, err = repository.RecordUncertain(ctx, operation.OperationID, operation.AuthorizationEpoch, model.SendFailureRateLimited, &retryAt, now)
	if err != nil || operation.State != model.SendUncertain || operation.FailureCode != model.SendFailureRateLimited || operation.RetryAt == nil || !operation.RetryAt.Equal(retryAt) {
		t.Fatalf("uncertain provider rejection operation=%#v error=%v", operation, err)
	}
	if _, dispatch, err := repository.BeginDispatch(ctx, operation.OperationID, operation.AuthorizationEpoch, now); err != nil || dispatch {
		t.Fatalf("uncertain operation retried dispatch=%v error=%v", dispatch, err)
	}
}

func TestOutgoingRestartRecoversInterruptedWithoutErasingReceipts(t *testing.T) {
	repository, database, path, now := outgoingFixture(t)
	ctx := context.Background()
	interrupted := requireAdmit(t, repository, outgoingCandidate(t, now, 123), now)
	confirmed := requireAdmit(t, repository, outgoingCandidate(t, now.Add(time.Millisecond), 124), now)
	if _, _, err := repository.BeginDispatch(ctx, interrupted.OperationID, interrupted.AuthorizationEpoch, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.RecordReceipt(ctx, confirmed.AuthorizationEpoch, confirmed.RandomID, 77, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	repository, _ = NewOutgoingRepository(reopened)
	if err := repository.RecoverInterrupted(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct {
		id    string
		state model.SendState
	}{{interrupted.OperationID, model.SendUncertain}, {confirmed.OperationID, model.SendSent}} {
		operation, exists, err := repository.Find(ctx, expected.id, now)
		if err != nil || !exists || operation.State != expected.state {
			t.Fatalf("recovered state=%v exists=%v error=%v", operation.State, exists, err)
		}
	}
	if _, _, err := repository.RecordReceipt(ctx, interrupted.AuthorizationEpoch, interrupted.RandomID, 78, nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestOutgoingRetentionCapacityAndExpiredKeys(t *testing.T) {
	repository, database, _, now := outgoingFixture(t)
	repository.capacity = 2
	ctx := context.Background()
	first := requireAdmit(t, repository, outgoingCandidate(t, now, 123), now)
	second := requireAdmit(t, repository, outgoingCandidate(t, now.Add(time.Millisecond), 124), now)
	third := outgoingCandidate(t, now.Add(2*time.Millisecond), 125)
	if _, _, err := repository.Admit(ctx, third, now); !errors.Is(err, ErrOutgoingCapacity) {
		t.Fatalf("full capacity error=%v", err)
	}
	if _, _, err := repository.BeginDispatch(ctx, first.OperationID, first.AuthorizationEpoch, now.Add(OutgoingRetryWindow)); !errors.Is(err, ErrOutgoingExpired) {
		t.Fatalf("expired dispatch error=%v", err)
	}
	if _, exists, err := repository.Find(ctx, first.OperationID, now.Add(2*24*time.Hour)); err != nil || !exists {
		t.Fatalf("retained status exists=%v error=%v", exists, err)
	}
	afterRetention := now.Add(OutgoingRetention + time.Second)
	if _, exists, err := repository.Find(ctx, first.OperationID, afterRetention); err != nil || exists {
		t.Fatalf("expired status exists=%v error=%v", exists, err)
	}
	if _, matched, err := repository.RecordReceipt(ctx, first.AuthorizationEpoch, first.RandomID, 77, nil, afterRetention); err != nil || matched {
		t.Fatalf("expired receipt matched=%v error=%v", matched, err)
	}
	newOperation := outgoingCandidate(t, afterRetention, 126)
	requireAdmit(t, repository, newOperation, afterRetention)
	if _, _, err := repository.Admit(ctx, first, afterRetention); !errors.Is(err, ErrOutgoingExpired) {
		t.Fatalf("pruned ID readmission error=%v", err)
	}
	var retained int
	if err := database.QueryRow("SELECT count(*) FROM outgoing_operations WHERE operation_id IN (?,?)", first.OperationID, second.OperationID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("pruned rows=%d error=%v", retained, err)
	}
}

func TestOutgoingWaitsAreMonotonicScopedAndTopicIndependent(t *testing.T) {
	repository, database, _, now := outgoingFixture(t)
	ctx := context.Background()
	parent, _ := model.NewPeerID(model.PeerKindChannel, 55)
	firstTopic, _ := model.NewTopicPeer(parent, 1)
	secondTopic, _ := model.NewTopicPeer(parent, 2)
	deadline := now.Add(48 * time.Hour)
	if err := repository.RecordWait(ctx, "test:2", 100, "messages.sendMessage", firstTopic, deadline, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordWait(ctx, "test:2", 100, "messages.sendMessage", secondTopic, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE authorization_state SET epoch=? WHERE singleton=1", strings.Repeat("b", 43)); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []model.PeerID{parent, firstTopic, secondTopic} {
		got, err := repository.WaitUntil(ctx, "test:2", 100, "messages.sendMessage", peer, now)
		if err != nil || !got.Equal(deadline) {
			t.Fatalf("topic wait=%v error=%v", got, err)
		}
	}
	for _, identity := range []struct {
		environment string
		account     int64
		method      string
	}{{"production", 100, "messages.sendMessage"}, {"test:1", 100, "messages.sendMessage"}, {"test:2", 101, "messages.sendMessage"}, {"test:2", 100, "contacts.resolveUsername"}} {
		got, err := repository.WaitUntil(ctx, identity.environment, identity.account, identity.method, parent, now)
		if err != nil || !got.IsZero() {
			t.Fatalf("unrelated identity wait=%v error=%v", got, err)
		}
	}
	global := deadline.Add(time.Hour)
	if err := repository.RecordWait(ctx, "test:2", 100, "messages.sendMessage", model.PeerID{}, global, now); err != nil {
		t.Fatal(err)
	}
	got, err := repository.WaitUntil(ctx, "test:2", 100, "messages.sendMessage", parent, now)
	if err != nil || !got.Equal(global) {
		t.Fatalf("global wait=%v error=%v", got, err)
	}
	got, err = repository.WaitUntil(ctx, "test:2", 100, "messages.sendMessage", parent, global)
	if err != nil || !got.IsZero() {
		t.Fatalf("elapsed wait=%v error=%v", got, err)
	}
}

func TestOutgoingPhoneSlotsAreAtomicAndPersistAcrossRepositories(t *testing.T) {
	repository, database, path, now := outgoingFixture(t)
	ctx := context.Background()
	var group sync.WaitGroup
	acquired := make(chan bool, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, ok, err := repository.ReservePhoneLookup(ctx, "production", 100, now)
			if err != nil {
				t.Errorf("ReservePhoneLookup() error=%v", err)
			}
			acquired <- ok
		}()
	}
	group.Wait()
	close(acquired)
	var count int
	for ok := range acquired {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("phone reservations=%d", count)
	}
	if deadline, err := repository.WaitUntil(ctx, "production", 100, "contacts.resolvePhone", model.PeerID{}, now); err != nil || !deadline.IsZero() {
		t.Fatalf("phone reservation blocked its own RPC deadline=%v error=%v", deadline, err)
	}
	providerWait := now.Add(time.Minute)
	if err := repository.RecordWait(ctx, "production", 100, "contacts.resolvePhone", model.PeerID{}, providerWait, now); err != nil {
		t.Fatal(err)
	}
	if deadline, err := repository.WaitUntil(ctx, "production", 100, "contacts.resolvePhone", model.PeerID{}, now); err != nil || !deadline.Equal(providerWait) {
		t.Fatalf("phone provider wait deadline=%v error=%v", deadline, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	other, _ := NewOutgoingRepository(reopened)
	deadline, ok, err := other.ReservePhoneLookup(ctx, "production", 100, now.Add(time.Second))
	if err != nil || ok || !deadline.Equal(now.Add(3*time.Second)) {
		t.Fatalf("persisted phone slot=%v acquired=%v error=%v", deadline, ok, err)
	}
	if _, ok, err := other.ReservePhoneLookup(ctx, "production", 100, now.Add(3*time.Second)); err != nil || !ok {
		t.Fatalf("next phone slot acquired=%v error=%v", ok, err)
	}
}

func TestOutgoingReceiptCanOutliveCallerCancellationUnderSQLiteContention(t *testing.T) {
	repository, _, path, now := outgoingFixture(t)
	operation := requireAdmit(t, repository, outgoingCandidate(t, now, 123), now)
	if _, _, err := repository.BeginDispatch(context.Background(), operation.OperationID, operation.AuthorizationEpoch, now); err != nil {
		t.Fatal(err)
	}
	blocker, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	transaction, err := blocker.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec("UPDATE outgoing_operations SET updated_at=updated_at"); err != nil {
		t.Fatal(err)
	}
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	receiptContext, receiptCancel := context.WithTimeout(context.WithoutCancel(caller), 5*time.Second)
	defer receiptCancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := repository.RecordReceipt(receiptContext, operation.AuthorizationEpoch, operation.RandomID, 77, nil, now)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("receipt did not wait for SQLite writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("cancellation-independent receipt error=%v", err)
	}
	confirmed, exists, err := repository.Find(context.Background(), operation.OperationID, now)
	if err != nil || !exists || confirmed.State != model.SendSent {
		t.Fatalf("retained receipt state=%v exists=%v error=%v", confirmed.State, exists, err)
	}
}

func TestOutgoingReceiptGraceBoundsSustainedSQLiteContention(t *testing.T) {
	testOutgoingReceiptDeadline(t, 5*time.Second, 0)
}

func TestOutgoingReceiptHonorsShortRemainingDeadlineUnderSQLiteContention(t *testing.T) {
	testOutgoingReceiptDeadline(t, 200*time.Millisecond, 0)
}

func TestOutgoingReceiptSharesDeadlineAcrossConnectionAndWriterWaits(t *testing.T) {
	testOutgoingReceiptDeadline(t, 400*time.Millisecond, 300*time.Millisecond)
}

func testOutgoingReceiptDeadline(t *testing.T, grace, connectionDelay time.Duration) {
	t.Helper()
	repository, _, path, now := outgoingFixture(t)
	operation := requireAdmit(t, repository, outgoingCandidate(t, now, 123), now)
	if _, _, err := repository.BeginDispatch(context.Background(), operation.OperationID, operation.AuthorizationEpoch, now); err != nil {
		t.Fatal(err)
	}
	blocker, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	transaction, err := blocker.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec("UPDATE outgoing_operations SET updated_at=updated_at"); err != nil {
		t.Fatal(err)
	}
	// Keep the production driver's busy timeout even when most of the metadata
	// grace has already elapsed before this write reaches SQLite.
	assertPragma(t, repository.database, "busy_timeout", "5000")
	var heldConnection *sql.Conn
	if connectionDelay > 0 {
		heldConnection, err = repository.database.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer heldConnection.Close()
	}
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	receiptContext, receiptCancel := context.WithTimeout(context.WithoutCancel(caller), grace)
	defer receiptCancel()
	type result struct {
		matched bool
		err     error
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		_, matched, err := repository.RecordReceipt(receiptContext, operation.AuthorizationEpoch, operation.RandomID, 77, nil, now)
		done <- result{matched: matched, err: err}
	}()
	if heldConnection != nil {
		<-time.After(connectionDelay)
		if err := heldConnection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case outcome := <-done:
		elapsed := time.Since(started)
		if outcome.err == nil || outcome.matched || elapsed > grace+150*time.Millisecond {
			t.Fatalf("blocked receipt matched=%v elapsed=%v error=%v", outcome.matched, elapsed, outcome.err)
		}
	case <-time.After(6 * time.Second):
		transaction.Rollback()
		<-done
		t.Fatal("receipt storage exceeded its metadata grace under sustained SQLite contention")
	}
	assertPragma(t, repository.database, "busy_timeout", "5000")
	retained, exists, err := repository.Find(context.Background(), operation.OperationID, now)
	if err != nil || !exists || retained.State != model.SendInFlight || retained.MessageID != nil {
		t.Fatalf("blocked receipt changed durable facts state=%v exists=%v error=%v", retained.State, exists, err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecoverInterrupted(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	retained, exists, err = repository.Find(context.Background(), operation.OperationID, now)
	if err != nil || !exists || retained.State != model.SendUncertain || retained.MessageID != nil {
		t.Fatalf("recovered blocked receipt state=%v exists=%v error=%v", retained.State, exists, err)
	}
}
