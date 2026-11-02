package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

// Only active requests retain transcription updates. There is no transcript
// cache, persisted job, or request replay after cancellation or restart.
type transcriptionWaiter struct {
	message  model.MessageID
	updates  chan *tg.UpdateTranscribedAudio
	overflow chan struct{}
}

func (r *readRuntime) watchTranscription(message model.MessageID) (*transcriptionWaiter, func(), error) {
	r.transcriptionMu.Lock()
	defer r.transcriptionMu.Unlock()
	if len(r.transcriptions) >= maximumConcurrentRequests {
		return nil, nil, model.TextError(model.ErrorTranscriptionUnavailable, nil)
	}
	for w := range r.transcriptions {
		if w.message == message {
			return nil, nil, model.TextError(model.ErrorTranscriptionPending, nil)
		}
	}
	w := &transcriptionWaiter{message: message, updates: make(chan *tg.UpdateTranscribedAudio, 4), overflow: make(chan struct{})}
	if r.transcriptions == nil {
		r.transcriptions = make(map[*transcriptionWaiter]struct{})
	}
	r.transcriptions[w] = struct{}{}
	return w, func() {
		r.transcriptionMu.Lock()
		defer r.transcriptionMu.Unlock()
		delete(r.transcriptions, w)
	}, nil
}

func (r *readRuntime) transcriptionUpdate(update *tg.UpdateTranscribedAudio) {
	// Partial text is never released or retained. Only final updates for an
	// active exact message can enter the bounded, request-local buffer.
	if update.Pending {
		return
	}
	r.transcriptionMu.Lock()
	defer r.transcriptionMu.Unlock()
	for w := range r.transcriptions {
		if update.MsgID != int(w.message.TelegramID()) || !matchesPeer(w.message.Peer(), update.Peer) {
			continue
		}
		select {
		case <-w.overflow:
			continue
		default:
		}
		if update.TranscriptionID <= 0 || (model.VoiceTranscript{Text: update.Text}).Validate() != nil {
			close(w.overflow)
			continue
		}
		copy := *update
		select {
		case w.updates <- &copy:
		default:
			close(w.overflow)
		}
	}
}

// TranscribeVoice can start a Telegram transcription job and consume the
// account's upstream allowance. The reader owns policy and read acknowledgment.
func (a *Account) TranscribeVoice(ctx context.Context, expected model.Candidate) (model.VoiceTranscript, error) {
	var result model.VoiceTranscript
	if expected.Voice == nil || expected.Image != nil || expected.Document != nil || !expected.Voice.IsVoice() {
		return result, model.TextError(model.ErrorInvalidReference, nil)
	}
	if _, err := a.exactMedia(ctx, expected); err != nil {
		return result, err
	}
	input, err := a.reads.inputPeer(ctx, expected.Message.ID.Peer())
	if err != nil {
		return result, err
	}
	// Subscribe before the RPC: a final update can arrive before its response.
	w, unwatch, err := a.reads.watchTranscription(expected.Message.ID)
	if err != nil {
		return result, err
	}
	defer unwatch()
	response, err := a.reads.api.MessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{Peer: input, MsgID: int(expected.Message.ID.TelegramID())})
	if err != nil {
		switch {
		case tgerr.Is(err, "PREMIUM_ACCOUNT_REQUIRED", "MSG_VOICE_TOO_LONG"):
			return result, model.TextError(model.ErrorTranscriptionUnavailable, err)
		case tgerr.Is(err, "TRANSCRIPTION_FAILED"):
			return result, model.TextError(model.ErrorTranscriptionFailed, err)
		case tgerr.Is(err, "MSG_ID_INVALID", "MSG_VOICE_MISSING", "PEER_ID_INVALID"):
			return result, model.TextError(model.ErrorInvalidReference, err)
		default:
			return result, readError(err)
		}
	}
	if response == nil || response.TranscriptionID <= 0 {
		return result, model.TextError(model.ErrorTranscriptionFailed, nil)
	}
	text := response.Text
	if response.Pending {
		// Keep the whole reader operation bounded, leaving time for source,
		// receipt and audit checks after asynchronous recognition completes.
		wait, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		for {
			select {
			case <-a.reads.failed:
				return result, model.TextError(model.ErrorFreshnessDegraded, a.reads.err())
			case <-w.overflow:
				return result, model.TextError(model.ErrorTranscriptionFailed, errors.New("Telegram transcription update budget or format is invalid"))
			case <-wait.Done():
				if ctx.Err() != nil {
					return result, readError(ctx.Err())
				}
				return result, model.TextError(model.ErrorTranscriptionPending, nil)
			case update := <-w.updates:
				if update.TranscriptionID != response.TranscriptionID {
					continue
				}
				text = update.Text
			}
			break
		}
		select {
		case <-w.overflow:
			return result, model.TextError(model.ErrorTranscriptionFailed, errors.New("Telegram transcription update budget or format is invalid"))
		default:
		}
	}
	result = model.VoiceTranscript{Text: text}
	if err := result.Validate(); err != nil {
		return model.VoiceTranscript{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.VoiceTranscript{}, readError(err)
	}
	if !a.Ready() {
		return model.VoiceTranscript{}, model.TextError(model.ErrorFreshnessDegraded, a.reads.err())
	}
	// The shared reader rechecks the exact source before and after its receipt.
	return result, nil
}
