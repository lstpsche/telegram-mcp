package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func batchHashUser(id, hash int64) *tg.User {
	user := &tg.User{ID: id}
	user.SetAccessHash(hash)
	return user
}

func TestUserHashBatchRefreshesOnlyCompleteNonzeroHashes(t *testing.T) {
	account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("hash persistence initiated an RPC")
		return nil
	})
	if err := account.reads.storage.SetUserAccessHash(context.Background(), 1, 2, 11); err != nil {
		t.Fatal(err)
	}
	minimal := batchHashUser(4, 44)
	minimal.Min = true
	users := []tg.UserClass{
		batchHashUser(2, 22), batchHashUser(3, 33), minimal,
		batchHashUser(5, 0), &tg.User{ID: 6}, &tg.UserEmpty{ID: 7},
		batchHashUser(0, 88), batchHashUser(2, 99),
	}
	if err := account.reads.persistUsers(context.Background(), users); err != nil || !account.Ready() {
		t.Fatal("valid user hash batch failed", err)
	}
	rows, err := db.Query("SELECT peer_id,access_hash FROM telegram_peer_hashes ORDER BY peer_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	expected := map[int64]int64{2: 99, 3: 33}
	for rows.Next() {
		var peer, hash int64
		if err := rows.Scan(&peer, &hash); err != nil {
			t.Fatal(err)
		}
		if expected[peer] != hash {
			t.Fatal("incomplete or zero hash replaced accepted metadata")
		}
		delete(expected, peer)
	}
	if err := rows.Err(); err != nil || len(expected) != 0 {
		t.Fatal("complete hash batch was not retained", err)
	}
}

func TestUserHashBatchRollsBackEveryWriteOnFailure(t *testing.T) {
	for _, behavior := range []string{"FAIL", "IGNORE"} {
		t.Run(behavior, func(t *testing.T) {
			account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { return nil })
			if err := account.reads.storage.SetUserAccessHash(context.Background(), 1, 2, 11); err != nil {
				t.Fatal(err)
			}
			raise := "RAISE(IGNORE)"
			if behavior == "FAIL" {
				raise = "RAISE(FAIL, 'synthetic user hash write failure')"
			}
			if _, err := db.Exec("CREATE TRIGGER reject_batch_hash BEFORE INSERT ON telegram_peer_hashes WHEN NEW.peer_id=3 BEGIN SELECT " + raise + "; END"); err != nil {
				t.Fatal(err)
			}
			err := account.reads.persistUsers(context.Background(), []tg.UserClass{batchHashUser(2, 22), batchHashUser(3, 33)})
			if err == nil || account.Ready() || account.reads.err() == nil {
				t.Fatal("failed hash batch left the runtime available")
			}
			var count, hash, pts int
			if err := db.QueryRow("SELECT count(*),max(access_hash) FROM telegram_peer_hashes").Scan(&count, &hash); err != nil || count != 1 || hash != 11 {
				t.Fatal("failed batch partially replaced hashes", err)
			}
			if err := db.QueryRow("SELECT pts FROM telegram_update_state").Scan(&pts); err != nil || pts != 10 {
				t.Fatal("hash batch changed the accepted checkpoint", err)
			}
			if _, err := db.Exec("DROP TRIGGER reject_batch_hash"); err != nil {
				t.Fatal(err)
			}
			if err := account.reads.persistUsers(context.Background(), []tg.UserClass{batchHashUser(4, 44)}); err == nil {
				t.Fatal("latched failure allowed a later hash batch")
			}
			if err := db.QueryRow("SELECT count(*) FROM telegram_peer_hashes").Scan(&count); err != nil || count != 1 {
				t.Fatal("later hash batch bypassed the runtime fence", err)
			}
		})
	}
}

func TestUserHashBatchRejectsStaleAuthorization(t *testing.T) {
	for _, change := range []string{"rotate", "delete"} {
		t.Run(change, func(t *testing.T) {
			account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { return nil })
			query := "DELETE FROM authorization_state"
			if change == "rotate" {
				query = "UPDATE authorization_state SET epoch='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'"
			}
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if err := account.reads.persistUsers(context.Background(), []tg.UserClass{batchHashUser(2, 22)}); err == nil || account.Ready() {
				t.Fatal("stale authorization accepted a hash batch")
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM telegram_peer_hashes").Scan(&count); err != nil || count != 0 {
				t.Fatal("stale authorization wrote hashes", err)
			}
		})
	}
}

func TestUserHashBatchCancellationPreservesRuntimeAndExistingHashes(t *testing.T) {
	account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { return nil })
	if err := account.reads.storage.SetUserAccessHash(context.Background(), 1, 2, 11); err != nil {
		t.Fatal(err)
	}
	err := cancelBlockedMetadataCall(t, db, func(ctx context.Context) error {
		return account.reads.persistUsers(ctx, []tg.UserClass{batchHashUser(2, 22), batchHashUser(3, 33)})
	})
	if !errors.Is(err, context.Canceled) || !account.Ready() || account.reads.err() != nil {
		t.Fatal("hash batch cancellation poisoned the runtime", err)
	}
	var count, hash int
	if err := db.QueryRow("SELECT count(*),max(access_hash) FROM telegram_peer_hashes").Scan(&count, &hash); err != nil || count != 1 || hash != 11 {
		t.Fatal("canceled hash batch changed metadata", err)
	}
	if err := account.reads.persistUsers(context.Background(), []tg.UserClass{batchHashUser(2, 22), batchHashUser(3, 33)}); err != nil || !account.Ready() {
		t.Fatal("later hash batch did not recover locally", err)
	}
}

func TestUserHashBatchRechecksRuntimeAfterConnectionWait(t *testing.T) {
	account, db := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { return nil })
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	before := db.Stats().WaitCount
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- account.reads.persistUsers(ctx, []tg.UserClass{batchHashUser(2, 22)}) }()
	deadline := time.Now().Add(2 * time.Second)
	for db.Stats().WaitCount == before {
		if time.Now().After(deadline) {
			t.Fatal("hash batch did not wait for the connection")
		}
		time.Sleep(time.Millisecond)
	}
	failure := errors.New("synthetic metadata failure")
	account.reads.fail(failure)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, failure) {
			t.Fatal("hash batch lost the runtime failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hash batch did not stop after the runtime failed")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM telegram_peer_hashes").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed runtime accepted a hash batch", err)
	}
}
