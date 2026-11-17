package telegram

import (
	"context"
	"errors"
	"math"
	"time"

	gotdauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

const receiptGrace = 5 * time.Second

type sendingRuntime struct {
	repository *store.OutgoingRepository
}
type sendCapture struct {
	attempt           model.SendAttempt
	receipt           *model.SendReceipt
	attempted         bool
	persistError      error
	recordingDeadline time.Time
}

func receiptRecordingContext(ctx context.Context, capture *sendCapture) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(receiptGrace)
	if capture != nil {
		if capture.recordingDeadline.IsZero() {
			capture.recordingDeadline = deadline
		}
		deadline = capture.recordingDeadline
	}
	return context.WithDeadline(context.WithoutCancel(ctx), deadline)
}

// EnableSending installs metadata-only receipt and pacing storage before the
// existing update manager starts. Human sending consent is enforced by callers.
func (a *Account) EnableSending(repository *store.OutgoingRepository) error {
	if a == nil || a.reads == nil || a.reads.storage == nil || repository == nil || a.reads.observed.Load() || a.reads.sending != nil {
		return model.TextError(model.ErrorNotReady, nil)
	}
	a.reads.sending = &sendingRuntime{repository: repository}
	return nil
}

func (a *Account) PrepareSend(ctx context.Context, attempt model.SendAttempt) (model.PreparedSend, error) {
	request, err := a.prepareSend(ctx, attempt)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (model.SendAttemptResult, error) {
		return a.sendPrepared(ctx, attempt, request)
	}, nil
}
func (a *Account) prepareSend(ctx context.Context, attempt model.SendAttempt) (*tg.MessagesSendMessageRequest, error) {
	if err := attempt.Validate(); err != nil {
		return nil, err
	}
	if !a.Ready() || a.reads.sending == nil {
		return nil, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	ctx = context.WithValue(ctx, outgoingContextKey{}, &outgoingCall{peer: attempt.Peer})
	now := time.Now().UTC()
	until, err := a.reads.sending.repository.WaitUntil(ctx, a.environment, a.reads.self.Load(), "messages.sendMessage", attempt.Peer.Parent(), now)
	if err != nil {
		a.reads.fail(errors.New("Telegram send pacing metadata is unavailable"))
		return nil, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if until.After(now) {
		return nil, sendRetryError(until)
	}
	_, broadcast, err := a.resolveKnownRecipient(ctx, attempt.Peer, true)
	if err != nil {
		return nil, err
	}
	input, err := a.reads.inputPeer(ctx, attempt.Peer)
	if err != nil {
		return nil, err
	}
	if attempt.ReplyTo != nil {
		if err := a.validateSendReply(ctx, attempt.Peer, input, *attempt.ReplyTo); err != nil {
			return nil, err
		}
	}
	request := &tg.MessagesSendMessageRequest{Peer: input, Message: attempt.Text, RandomID: attempt.RandomID, NoWebpage: true}
	if attempt.Peer.Kind() == model.PeerKindChat || attempt.Peer.Kind() == model.PeerKindChannel {
		if broadcast {
			request.SetSendAs(input)
		} else {
			request.SetSendAs(&tg.InputPeerSelf{})
		}
	}
	replyID := 0
	if attempt.ReplyTo != nil {
		replyID = int(attempt.ReplyTo.TelegramID())
	} else if attempt.Peer.TopicID() > 1 {
		replyID = int(attempt.Peer.TopicID())
	}
	if replyID > 0 {
		reply := &tg.InputReplyToMessage{ReplyToMsgID: replyID}
		if attempt.Peer.TopicID() > 1 && replyID != int(attempt.Peer.TopicID()) {
			reply.SetTopMsgID(int(attempt.Peer.TopicID()))
		}
		request.SetReplyTo(reply)
	}
	return request, nil
}

func (a *Account) sendPrepared(ctx context.Context, attempt model.SendAttempt, request *tg.MessagesSendMessageRequest) (model.SendAttemptResult, error) {
	if err := ctx.Err(); err != nil {
		return model.SendAttemptResult{}, err
	}
	if !a.Ready() || a.reads.sending == nil {
		return model.SendAttemptResult{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if err := a.reads.storage.checkEpoch(ctx); err != nil {
		return model.SendAttemptResult{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	capture := &sendCapture{attempt: attempt}
	bounded = context.WithValue(bounded, outgoingContextKey{}, &outgoingCall{peer: attempt.Peer, capture: capture})
	_, err := a.reads.api.MessagesSendMessage(bounded, request)
	result := model.SendAttemptResult{Receipt: capture.receipt, Attempted: capture.attempted}
	if capture.persistError != nil {
		result.FailureCode = model.SendFailureReceiptNotPersisted
		return result, &model.SendError{Category: model.ErrorReceiptNotPersisted, FailureCode: result.FailureCode, Cause: capture.persistError, MetadataDeadline: capture.recordingDeadline}
	}
	if err != nil {
		if result.Receipt != nil {
			a.reads.fail(errors.New("Telegram send update synchronization failed"))
			result.FailureCode = model.SendFailureStateSyncFailed
			return result, &model.SendError{Category: model.ErrorStateSyncFailed, FailureCode: result.FailureCode}
		}
		err = outgoingError(err)
		applySendError(&result, err)
		// The SDK may replay an unacknowledged request on a new connection.
		// A later rejection cannot disprove an earlier send with this random ID.
		result.Uncertain = result.Attempted
		if result.Attempted && result.FailureCode == "" {
			result.Uncertain = true
			result.FailureCode = model.SendFailureEffectUncertain
			err = &model.SendError{Category: model.ErrorSendEffectUncertain, FailureCode: result.FailureCode, Cause: err}
		}
		return result, withRecordingDeadline(err, capture.recordingDeadline)
	}
	if result.Receipt == nil {
		result.Uncertain = result.Attempted
		result.FailureCode = model.SendFailureEffectUncertain
		return result, &model.SendError{Category: model.ErrorSendEffectUncertain, FailureCode: result.FailureCode, MetadataDeadline: capture.recordingDeadline}
	}
	return result, nil
}

func withRecordingDeadline(err error, deadline time.Time) error {
	if deadline.IsZero() {
		return err
	}
	var failure *model.SendError
	if errors.As(err, &failure) {
		copy := *failure
		if copy.MetadataDeadline.IsZero() || deadline.Before(copy.MetadataDeadline) {
			copy.MetadataDeadline = deadline
		}
		return &copy
	}
	return &model.SendError{Category: model.TextErrorCategory(err), Cause: err, MetadataDeadline: deadline}
}

func outgoingError(err error) error {
	if errors.Is(err, model.ErrSendMetadataRecording) {
		return err
	}
	var send *model.SendError
	if errors.As(err, &send) {
		return send
	}
	if gotdauth.IsUnauthorized(err) {
		return model.TextError(model.ErrorReauthRequired, nil)
	}
	if rpc, ok := tgerr.As(err); ok {
		category, code := model.ErrorTelegramUnavailable, model.SendFailureCode("")
		switch rpc.Type {
		case "MESSAGE_TOO_LONG":
			category, code = model.ErrorInvalidInput, model.SendFailureMessageTooLong
		case "CHAT_WRITE_FORBIDDEN", "CHAT_ADMIN_REQUIRED", "USER_IS_BLOCKED", "YOU_BLOCKED_USER", "CHAT_SEND_PLAIN_FORBIDDEN", "CHAT_SEND_MESSAGES_FORBIDDEN", "TOPIC_CLOSED", "USER_BANNED_IN_CHANNEL", "SEND_AS_PEER_INVALID", "SEND_AS_PEER_ID_INVALID":
			category, code = model.ErrorWriteForbidden, model.SendFailureWriteForbidden
		case "ALLOW_PAYMENT_REQUIRED", "PAYMENT_REQUIRED", "STARS_PAYMENT_REQUIRED":
			category, code = model.ErrorPaymentRequired, model.SendFailurePaymentRequired
		case "PEER_FLOOD", "USER_RESTRICTED", "FROZEN_METHOD_INVALID":
			category, code = model.ErrorAccountRestricted, model.SendFailureAccountRestricted
		case "PEER_ID_INVALID", "USERNAME_INVALID", "USERNAME_NOT_OCCUPIED", "PHONE_NOT_OCCUPIED", "PHONE_NUMBER_INVALID", "CHANNEL_PRIVATE", "CHANNEL_INVALID", "CHAT_ID_INVALID", "INPUT_USER_DEACTIVATED":
			category, code = model.ErrorRecipientUnavailable, model.SendFailureRecipientUnavailable
		case "REPLY_MESSAGE_ID_INVALID", "MSG_ID_INVALID", "TOPIC_ID_INVALID":
			category, code = model.ErrorInvalidReference, model.SendFailureInvalidReference
		}
		return &model.SendError{Category: category, FailureCode: code}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return model.TextError(model.ErrorTelegramUnavailable, nil)
}
func applySendError(result *model.SendAttemptResult, err error) {
	var typed *model.SendError
	if errors.As(err, &typed) {
		result.FailureCode = typed.FailureCode
		result.RetryAt = typed.RetryAt
	}
}

func (a *Account) validateSendReply(ctx context.Context, peer model.PeerID, input tg.InputPeerClass, reply model.MessageID) error {
	if reply.Peer() != peer {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: int(reply.TelegramID())}}
	var response tg.MessagesMessagesClass
	var err error
	if channel, ok := input.(*tg.InputPeerChannel); ok {
		response, err = a.reads.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, ID: ids})
	} else {
		response, err = a.reads.api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return outgoingError(err)
	}
	page, ok := response.(messagePage)
	if !ok || len(page.GetMessages()) != 1 || len(page.GetUsers()) > 200 || len(page.GetChats()) > 200 || len(page.GetTopics()) > 100 {
		return invalidSendReply()
	}
	message := page.GetMessages()[0]
	if message.GetID() != int(reply.TelegramID()) {
		return invalidSendReply()
	}
	var actual tg.PeerClass
	var header tg.MessageReplyHeaderClass
	root := false
	switch message := message.(type) {
	case *tg.Message:
		actual, header = message.PeerID, message.ReplyTo
	case *tg.MessageService:
		actual, header = message.PeerID, message.ReplyTo
		_, root = message.Action.(*tg.MessageActionTopicCreate)
	default:
		return invalidSendReply()
	}
	if !matchesPeer(peer, actual) || !sendReplyRoute(peer, message.GetID(), header, root) {
		return invalidSendReply()
	}
	return nil
}
func invalidSendReply() error {
	return &model.SendError{Category: model.ErrorInvalidReference, FailureCode: model.SendFailureInvalidReference}
}
func sendReplyRoute(peer model.PeerID, id int, header tg.MessageReplyHeaderClass, root bool) bool {
	if root {
		return peer.TopicID() > 0 && id == int(peer.TopicID())
	}
	if header == nil {
		return peer.TopicID() <= 1
	}
	reply, ok := header.(*tg.MessageReplyHeader)
	if !ok {
		return false
	}
	if peer.TopicID() == 0 {
		return !reply.ForumTopic
	}
	if !reply.ForumTopic {
		return peer.TopicID() == 1 && reply.ReplyToTopID == 0
	}
	topic := reply.ReplyToTopID
	if topic == 0 {
		// A quoted message's external origin cannot identify a local topic.
		if reply.ReplyToPeerID != nil && !matchesPeer(peer, reply.ReplyToPeerID) {
			return false
		}
		topic = reply.ReplyToMsgID
	}
	return topic == int(peer.TopicID())
}

func updateItems(updates tg.UpdatesClass) []tg.UpdateClass {
	switch updates := updates.(type) {
	case *tg.Updates:
		return updates.Updates
	case *tg.UpdatesCombined:
		return updates.Updates
	case *tg.UpdateShort:
		return []tg.UpdateClass{updates.Update}
	}
	return nil
}
func validMappings(items []tg.UpdateClass) (map[int64]int32, error) {
	mappings := map[int64]int32{}
	for _, item := range items {
		if mapping, ok := item.(*tg.UpdateMessageID); ok {
			if mapping.RandomID == 0 || mapping.ID <= 0 || mapping.ID > math.MaxInt32 {
				return nil, errors.New("Telegram send mapping is invalid")
			}
			if previous, found := mappings[mapping.RandomID]; found && previous != int32(mapping.ID) {
				return nil, errors.New("Telegram send mappings conflict")
			}
			mappings[mapping.RandomID] = int32(mapping.ID)
		}
	}
	return mappings, nil
}
func (r *readRuntime) captureMappings(ctx context.Context, items []tg.UpdateClass) error {
	if r.sending == nil {
		return nil
	}
	mappings, err := validMappings(items)
	if err != nil {
		r.fail(err)
		return model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	call, _ := ctx.Value(outgoingContextKey{}).(*outgoingCall)
	if call != nil && call.capture != nil && call.capture.persistError == nil {
		if receipt := call.capture.receipt; receipt != nil && mappings[receipt.RandomID] == receipt.MessageID.TelegramID() {
			delete(mappings, receipt.RandomID)
		}
	}
	if len(mappings) == 0 {
		return nil
	}
	var capture *sendCapture
	if call != nil {
		capture = call.capture
	}
	local, cancel := receiptRecordingContext(ctx, capture)
	defer cancel()
	for randomID, id := range mappings {
		if _, _, err := r.sending.repository.RecordReceipt(local, r.storage.epoch, randomID, id, nil, time.Now().UTC()); err != nil {
			r.fail(errors.New("Telegram send receipt could not be persisted"))
			deadline, _ := local.Deadline()
			return &model.SendError{Category: model.ErrorReceiptNotPersisted, FailureCode: model.SendFailureReceiptNotPersisted, Cause: errors.Join(model.ErrSendMetadataRecording, err), MetadataDeadline: deadline}
		}
	}
	return nil
}
func (r *readRuntime) captureAcknowledgment(ctx context.Context, capture *sendCapture, updates tg.UpdatesClass) error {
	var id int32
	var date int
	if short, ok := updates.(*tg.UpdateShortSentMessage); ok {
		if !short.Out || short.ID <= 0 || short.ID > math.MaxInt32 || short.Date <= 0 {
			return model.TextError(model.ErrorInvalidReference, nil)
		}
		id, date = int32(short.ID), short.Date
	} else {
		items := updateItems(updates)
		mappings, err := validMappings(items)
		if err != nil {
			return model.TextError(model.ErrorInvalidReference, nil)
		}
		id = mappings[capture.attempt.RandomID]
		if id == 0 {
			return nil
		}
		for _, item := range items {
			var message tg.MessageClass
			switch item := item.(type) {
			case *tg.UpdateNewMessage:
				message = item.Message
			case *tg.UpdateNewChannelMessage:
				message = item.Message
			}
			if message == nil || message.GetID() != int(id) {
				continue
			}
			m, ok := message.(*tg.Message)
			if !ok || !m.Out || !matchesPeer(capture.attempt.Peer, m.PeerID) {
				return model.TextError(model.ErrorInvalidReference, nil)
			}
			date = m.Date
		}
	}
	messageID, err := model.NewMessageID(capture.attempt.Peer, id)
	if err != nil {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	receipt := &model.SendReceipt{Peer: capture.attempt.Peer, RandomID: capture.attempt.RandomID, MessageID: messageID}
	var sentAt *time.Time
	if date > 0 {
		value := time.Unix(int64(date), 0).UTC()
		sentAt = &value
		receipt.SentAt = value.Format(time.RFC3339Nano)
	}
	capture.receipt = receipt
	local, cancel := receiptRecordingContext(ctx, capture)
	defer cancel()
	operation, found, err := r.sending.repository.RecordReceipt(local, r.storage.epoch, capture.attempt.RandomID, id, sentAt, time.Now().UTC())
	if err != nil || !found || operation.Peer != capture.attempt.Peer {
		if errors.Is(err, store.ErrOutgoingConflict) {
			capture.receipt = nil
			if operation.State == model.SendSent && operation.Peer == capture.attempt.Peer && operation.RandomID == capture.attempt.RandomID && operation.MessageID != nil && operation.MessageID.Peer() == capture.attempt.Peer {
				known := &model.SendReceipt{Peer: operation.Peer, RandomID: operation.RandomID, MessageID: *operation.MessageID}
				if operation.SentAt != nil {
					known.SentAt = operation.SentAt.UTC().Format(time.RFC3339Nano)
				}
				capture.receipt = known
			}
		}
		capture.persistError = errors.Join(model.ErrSendMetadataRecording, errors.New("Telegram send receipt could not be persisted"))
		r.fail(capture.persistError)
		return &model.SendError{Category: model.ErrorReceiptNotPersisted, FailureCode: model.SendFailureReceiptNotPersisted}
	}
	return nil
}
