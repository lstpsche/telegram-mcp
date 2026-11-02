package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func transcriptionTestAccount(t *testing.T, transcribe func(context.Context, *Account, *tg.MessagesTranscribeAudioRequest) (*tg.MessagesTranscribedAudio, error)) (*Account, model.Candidate) {
	t.Helper()
	message := voiceMessage()
	var account *Account
	invoke := gotdtelegram.InvokeFunc(func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesGetHistoryRequest:
			return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{message}})
		case *tg.MessagesTranscribeAudioRequest:
			if _, ok := q.Peer.(*tg.InputPeerSelf); !ok || q.MsgID != message.ID {
				t.Fatal("transcription lost exact message identity")
			}
			result, err := transcribe(ctx, account, q)
			if err != nil {
				return err
			}
			return encodeReadResponse(out, result)
		default:
			t.Fatalf("unexpected transcription RPC %T", input)
			return nil
		}
	})
	account, _ = newReadTestAccount(t, invoke)
	return account, imageCandidate(t, message)
}

func TestTelegramTranscriptionAcceptsImmediateAndEarlyFinalUpdates(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "early_update"}[early], func(t *testing.T) {
			calls := 0
			account, candidate := transcriptionTestAccount(t, func(ctx context.Context, a *Account, q *tg.MessagesTranscribeAudioRequest) (*tg.MessagesTranscribedAudio, error) {
				calls++
				if early {
					for _, update := range []*tg.UpdateTranscribedAudio{
						{Peer: &tg.PeerUser{UserID: 2}, MsgID: q.MsgID, TranscriptionID: 44, Text: "wrong peer"},
						{Peer: &tg.PeerUser{UserID: 1}, MsgID: q.MsgID + 1, TranscriptionID: 44, Text: "wrong message"},
						{Peer: &tg.PeerUser{UserID: 1}, MsgID: q.MsgID, TranscriptionID: 43, Text: "old transcription"},
						{Peer: &tg.PeerUser{UserID: 1}, MsgID: q.MsgID, TranscriptionID: 44, Pending: true, Text: "partial"},
						{Peer: &tg.PeerUser{UserID: 1}, MsgID: q.MsgID, TranscriptionID: 44, Text: "Synthetic final transcript."},
					} {
						if err := a.reads.discardUpdates(ctx, &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
							t.Fatal(err)
						}
					}
				}
				return &tg.MessagesTranscribedAudio{TranscriptionID: 44, Text: "Synthetic final transcript.", Pending: early}, nil
			})
			result, err := account.TranscribeVoice(context.Background(), candidate)
			if err != nil || result.Text != "Synthetic final transcript." || calls != 1 {
				t.Fatalf("transcription failed: %v", err)
			}
			if len(account.reads.transcriptions) != 0 {
				t.Fatal("subscription retained content")
			}
		})
	}
}

func TestTelegramTranscriptionFailuresAreTypedAndNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rpcErr   error
		response *tg.MessagesTranscribedAudio
		want     model.ErrorCategory
	}{
		{name: "allowance", rpcErr: tgerr.New(400, "PREMIUM_ACCOUNT_REQUIRED"), want: model.ErrorTranscriptionUnavailable},
		{name: "duration", rpcErr: tgerr.New(400, "MSG_VOICE_TOO_LONG"), want: model.ErrorTranscriptionUnavailable},
		{name: "failed", rpcErr: tgerr.New(400, "TRANSCRIPTION_FAILED"), want: model.ErrorTranscriptionFailed},
		{name: "missing", rpcErr: tgerr.New(400, "MSG_VOICE_MISSING"), want: model.ErrorInvalidReference},
		{name: "invalid_id", response: &tg.MessagesTranscribedAudio{}, want: model.ErrorTranscriptionFailed},
		{name: "oversize", response: &tg.MessagesTranscribedAudio{TranscriptionID: 44, Text: strings.Repeat("x", model.MaximumVoiceTranscriptBytes+1)}, want: model.ErrorResultTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			account, candidate := transcriptionTestAccount(t, func(context.Context, *Account, *tg.MessagesTranscribeAudioRequest) (*tg.MessagesTranscribedAudio, error) {
				calls++
				return tc.response, tc.rpcErr
			})
			result, err := account.TranscribeVoice(context.Background(), candidate)
			if model.TextErrorCategory(err) != tc.want || result.Text != "" || calls != 1 || len(account.reads.transcriptions) != 0 {
				t.Fatalf("unexpected failure: %v", err)
			}
		})
	}
}

func TestTelegramTranscriptionCancellationAndRuntimeFailureWithholdText(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, degraded := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			account, candidate := transcriptionTestAccount(t, func(_ context.Context, a *Account, _ *tg.MessagesTranscribeAudioRequest) (*tg.MessagesTranscribedAudio, error) {
				if degraded {
					a.reads.fail(errors.New("synthetic freshness failure"))
				} else {
					cancel()
				}
				return &tg.MessagesTranscribedAudio{Pending: pending, TranscriptionID: 44, Text: "must not escape"}, nil
			})
			result, err := account.TranscribeVoice(ctx, candidate)
			cancel()
			want := model.ErrorCancelled
			if degraded {
				want = model.ErrorFreshnessDegraded
			}
			if model.TextErrorCategory(err) != want || result.Text != "" || len(account.reads.transcriptions) != 0 {
				t.Fatalf("text escaped after cancellation or runtime failure (pending=%t, degraded=%t): %v", pending, degraded, err)
			}
		}
	}
}

func TestTelegramTranscriptionSubscriptionsAreBounded(t *testing.T) {
	r := &readRuntime{}
	peer := testSelfPeer(t)
	for i := int32(1); i <= maximumConcurrentRequests; i++ {
		id, _ := model.NewMessageID(peer, i)
		w, unwatch, err := r.watchTranscription(id)
		if err != nil {
			t.Fatal(err)
		}
		defer unwatch()
		if _, _, err := r.watchTranscription(id); err == nil {
			t.Fatal("duplicate request accepted")
		}
		for j := 0; j < 5; j++ {
			r.transcriptionUpdate(&tg.UpdateTranscribedAudio{Peer: &tg.PeerUser{UserID: 1}, MsgID: int(i), TranscriptionID: int64(j + 1), Text: "synthetic"})
		}
		select {
		case <-w.overflow:
		default:
			t.Fatal("unbounded update stream accepted")
		}
	}
	id, _ := model.NewMessageID(peer, 99)
	if _, _, err := r.watchTranscription(id); err == nil {
		t.Fatal("unbounded subscriptions accepted")
	}
}

func TestTelegramTranscriptionAwaitsAsynchronousFinalText(t *testing.T) {
	account, candidate := transcriptionTestAccount(t, func(ctx context.Context, a *Account, q *tg.MessagesTranscribeAudioRequest) (*tg.MessagesTranscribedAudio, error) {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			a.reads.transcriptionUpdate(&tg.UpdateTranscribedAudio{Peer: &tg.PeerUser{UserID: 1}, MsgID: q.MsgID, TranscriptionID: 44, Text: "Synthetic asynchronous transcript."})
		}()
		return &tg.MessagesTranscribedAudio{Pending: true, TranscriptionID: 44, Text: "partial must not escape"}, nil
	})
	result, err := account.TranscribeVoice(context.Background(), candidate)
	if err != nil || result.Text != "Synthetic asynchronous transcript." {
		t.Fatalf("final update not awaited: %v", err)
	}
}

func TestTelegramTranscriptionPendingDoesNotInventFinalText(t *testing.T) {
	account, candidate := transcriptionTestAccount(t, func(context.Context, *Account, *tg.MessagesTranscribeAudioRequest) (*tg.MessagesTranscribedAudio, error) {
		return &tg.MessagesTranscribedAudio{Pending: true, TranscriptionID: 44, Text: "partial must not escape"}, nil
	})
	result, err := account.TranscribeVoice(context.Background(), candidate)
	if model.TextErrorCategory(err) != model.ErrorTranscriptionPending || result.Text != "" || len(account.reads.transcriptions) != 0 {
		t.Fatalf("pending recognition mishandled: %v", err)
	}
}
