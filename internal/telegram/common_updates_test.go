package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestMixedUpdatesPreserveCommonSequenceAcrossRecoveryAndRestart(t *testing.T) {
	invoke := func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetDifferenceRequest:
			if q.Pts != 10 && q.Pts != 13 {
				t.Error("unexpected persisted common checkpoint")
			}
			return encodeReadResponse(out, &tg.UpdatesDifference{
				OtherUpdates: []tg.UpdateClass{&tg.UpdateChannelTooLong{ChannelID: 7}},
				Chats:        []tg.ChatClass{&tg.ChannelForbidden{ID: 7, AccessHash: 77, Title: "synthetic"}},
				State:        tg.UpdatesState{Pts: q.Pts, Date: q.Date, Seq: 1},
			})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		}
		t.Errorf("unexpected RPC type %T", input)
		return errors.New("unexpected RPC")
	}
	account, db := newReadTestAccount(t, invoke)
	ready, done, cancel := observeRecovery(t, account)
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	}
	batch := &tg.UpdatesCombined{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{Message: testMessage(5), Pts: 11, PtsCount: 1},
			&tg.UpdateDeleteChannelMessages{ChannelID: 7, Messages: []int{1}, Pts: 900, PtsCount: 1},
			&tg.UpdateChannelTooLong{ChannelID: 7},
			&tg.UpdateChannelParticipant{ChannelID: 7, Qts: 1},
			&tg.UpdateNewMessage{Message: testMessage(6), Pts: 12, PtsCount: 1},
		},
		Chats: []tg.ChatClass{&tg.ChannelForbidden{ID: 7, AccessHash: 77, Title: "synthetic"}},
		Date:  101, SeqStart: 2, Seq: 2,
	}
	if err := account.reads.Handle(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if err := account.reads.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateChannelTooLong{ChannelID: 7}}, Date: 102, Seq: 3}); err != nil {
		t.Fatal(err)
	}
	if err := account.reads.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: testMessage(7), Pts: 13, PtsCount: 1}}, Date: 103, Seq: 4}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var pts, qts, seq int
		if err := db.QueryRow("SELECT pts,qts,seq FROM telegram_update_state").Scan(&pts, &qts, &seq); err != nil {
			t.Fatal(err)
		}
		if pts == 13 && qts == 1 && seq == 4 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("mixed updates did not advance common sequence")
		case err := <-done:
			t.Fatal(err)
		}
	}
	if len(batch.Updates) != 5 || len(batch.Chats) != 1 {
		t.Fatal("projection mutated original response")
	}
	for _, table := range []string{"telegram_channel_state", "telegram_peer_hashes WHERE kind='channel'"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("channel metadata retained", err)
		}
	}
	cancel()
	<-done
	restarted := restartReadAccount(t, db, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetDifferenceRequest:
			if q.Pts != 13 || q.Date != 103 {
				t.Error("restart lost common checkpoint")
			}
			return encodeReadResponse(out, &tg.UpdatesDifference{OtherUpdates: []tg.UpdateClass{&tg.UpdateChannelTooLong{ChannelID: 7}}, State: tg.UpdatesState{Pts: 13, Qts: 1, Date: 103, Seq: 4}})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 13, Qts: 1, Date: 103, Seq: 4})
		}
		t.Errorf("unexpected restart RPC type %T", input)
		return errors.New("unexpected RPC")
	})
	ready, done, _ = observeRecovery(t, restarted)
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	}
}

func TestChannelRecoveryStillCountsAgainstResponseBudget(t *testing.T) {
	response := &tg.UpdatesDifference{State: tg.UpdatesState{Pts: 12, Date: 101, Seq: 1}}
	for range 101 {
		response.OtherUpdates = append(response.OtherUpdates, &tg.UpdateChannelTooLong{ChannelID: 7})
	}
	account, _ := newReadTestAccount(t, func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
		return encodeReadResponse(out, response)
	})
	if result, err := account.reads.UpdatesGetDifference(context.Background(), &tg.UpdatesGetDifferenceRequest{Pts: 10, Date: 100}); err == nil || result != nil {
		t.Fatal("filtering bypassed recovery budget")
	}
}

func TestMalformedChannelEventFailsBeforeCheckpointAndMetadata(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "recovery"}[recovery], func(t *testing.T) {
			event := &tg.UpdateNewChannelMessage{Message: testMessage(5), Pts: 900, PtsCount: 1}
			user := &tg.User{ID: 2}
			user.SetAccessHash(22)
			account, db := newReadTestAccount(t, func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
				return encodeReadResponse(out, &tg.UpdatesDifference{OtherUpdates: []tg.UpdateClass{event}, Users: []tg.UserClass{user}, State: tg.UpdatesState{Pts: 12, Date: 101, Seq: 2}})
			})
			var err error
			if recovery {
				var result tg.UpdatesDifferenceClass
				result, err = account.reads.UpdatesGetDifference(context.Background(), &tg.UpdatesGetDifferenceRequest{Pts: 10, Date: 100})
				if result != nil {
					t.Fatal("malformed recovery returned data")
				}
			} else {
				err = account.reads.Handle(context.Background(), &tg.UpdateShort{Update: event, Date: 101})
			}
			if err == nil || account.Ready() {
				t.Fatal("malformed channel event ignored")
			}
			var pts, hashes int
			if err := db.QueryRow("SELECT pts FROM telegram_update_state").Scan(&pts); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT count(*) FROM telegram_peer_hashes").Scan(&hashes); err != nil {
				t.Fatal(err)
			}
			if pts != 10 || hashes != 0 {
				t.Fatal("malformed event changed metadata")
			}
		})
	}
}
