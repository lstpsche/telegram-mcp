package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

// Only adapter-owned outgoing calls bypass the legacy read flood waiter. A
// method name cannot confer this authority because reads also resolve names.
type outgoingContextKey struct{}
type outgoingCall struct {
	peer    model.PeerID
	capture *sendCapture
}

type outgoingWaiter struct{ legacy gotdtelegram.Middleware }

func (w outgoingWaiter) Handle(next tg.Invoker) gotdtelegram.InvokeFunc {
	legacy := w.legacy.Handle(next)
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		if ctx.Value(outgoingContextKey{}) != nil {
			return next.Invoke(ctx, input, output)
		}
		return legacy.Invoke(ctx, input, output)
	}
}

type rpcControl struct {
	runtime     *readRuntime
	environment string
}

func metadataCancellation(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		canceled := false
		for _, cause := range joined.Unwrap() {
			if cause == nil {
				continue
			}
			if !metadataCancellation(cause) {
				return false
			}
			canceled = true
		}
		return canceled
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return metadataCancellation(wrapped.Unwrap())
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (r *readRuntime) metadataError(err error) error {
	if err == nil || metadataCancellation(err) {
		return err
	}
	r.fail(err)
	return model.TextError(model.ErrorFreshnessDegraded, err)
}

func controlledMethod(input bin.Encoder) string {
	switch input.(type) {
	case *tg.ContactsResolveUsernameRequest:
		return "contacts.resolveUsername"
	case *tg.ContactsResolvePhoneRequest:
		return "contacts.resolvePhone"
	case *tg.MessagesSendMessageRequest:
		return "messages.sendMessage"
	case *tg.UsersGetUsersRequest:
		return "users.getUsers"
	case *tg.ChannelsGetChannelsRequest:
		return "channels.getChannels"
	case *tg.MessagesGetChatsRequest:
		return "messages.getChats"
	case *tg.MessagesGetForumTopicsByIDRequest:
		return "messages.getForumTopicsByID"
	case *tg.ChannelsGetMessagesRequest:
		return "channels.getMessages"
	case *tg.MessagesGetMessagesRequest:
		return "messages.getMessages"
	default:
		return ""
	}
}
func (c rpcControl) Handle(next tg.Invoker) gotdtelegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		r := c.runtime
		method := controlledMethod(input)
		if r == nil || r.sending == nil || method == "" || r.self.Load() <= 0 {
			return next.Invoke(ctx, input, output)
		}
		call, _ := ctx.Value(outgoingContextKey{}).(*outgoingCall)
		var peer model.PeerID
		if call != nil {
			peer = call.peer.Parent()
		}
		now := time.Now().UTC()
		until, err := r.sending.repository.WaitUntil(ctx, c.environment, r.self.Load(), method, peer, now)
		if err != nil {
			return r.metadataError(err)
		}
		if until.After(now) {
			if call != nil {
				return sendRetryError(until)
			}
			if until.Sub(now) > maximumFloodWait {
				// Read tools retain the legacy waiter's bounded availability contract.
				return model.TextError(model.ErrorTelegramUnavailable, nil)
			}
			timer := time.NewTimer(until.Sub(now))
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if method == "contacts.resolvePhone" {
			until, acquired, err := r.sending.repository.ReservePhoneLookup(ctx, c.environment, r.self.Load(), time.Now().UTC())
			if err != nil {
				return r.metadataError(err)
			}
			if !acquired {
				return sendRetryError(until)
			}
		}
		err = next.Invoke(ctx, input, output)
		wait, ok := tgerr.AsFloodWait(err)
		slow := false
		if rpc, yes := tgerr.AsType(err, "SLOWMODE_WAIT"); yes {
			wait = time.Duration(rpc.Argument) * time.Second
			ok = true
			slow = true
		}
		if !ok || wait <= 0 {
			return err
		}
		until = time.Now().UTC().Add(wait)
		if !slow {
			peer = model.PeerID{}
		}
		var capture *sendCapture
		if call != nil {
			capture = call.capture
		}
		local, cancel := receiptRecordingContext(ctx, capture)
		defer cancel()
		if storeErr := r.sending.repository.RecordWait(local, c.environment, r.self.Load(), method, peer, until, time.Now().UTC()); storeErr != nil {
			r.fail(errors.New("Telegram RPC wait could not be persisted"))
			return model.TextError(model.ErrorFreshnessDegraded, errors.Join(model.ErrSendMetadataRecording, storeErr))
		}
		if call != nil {
			retry := sendRetryError(until)
			retry.MetadataDeadline, _ = local.Deadline()
			return retry
		}
		return err
	}
}

func sendRetryError(at time.Time) *model.SendError {
	return &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, RetryAt: at, Cause: model.TextError(model.ErrorRateLimited, nil)}
}
