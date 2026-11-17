package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func newSendingTestAccount(t *testing.T, invoke gotdtelegram.InvokeFunc) (*Account, *store.OutgoingRepository, *sql.DB) {
	t.Helper()
	account, db := newReadTestAccount(t, invoke)
	repository, err := store.NewOutgoingRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.EnableSending(repository); err != nil {
		t.Fatal(err)
	}
	account.reads.api = tg.NewClient(rpcControl{runtime: account.reads, environment: account.environment}.Handle(readMiddleware{account.reads}.Handle(invoke)))
	return account, repository, db
}

func admitSendingTestAttempt(t *testing.T, repository *store.OutgoingRepository, peer model.PeerID, reply *model.MessageID) model.SendAttempt {
	t.Helper()
	now := time.Now().UTC()
	id, err := model.NewOperationID(now, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	attempt := model.SendAttempt{OperationID: id, Peer: peer, ReplyTo: reply, RandomID: 987654321, Text: "  Synthetic text\nhttps://example.invalid/  "}
	_, created, err := repository.Admit(context.Background(), store.OutgoingOperation{
		OperationID: id, AuthorizationEpoch: readTestEpoch, Peer: peer, ReplyTo: reply,
		RandomID: attempt.RandomID, PayloadDigest: [32]byte{1},
	}, now)
	if err != nil || !created {
		t.Fatalf("admit send: created=%v err=%v", created, err)
	}
	if _, changed, err := repository.BeginDispatch(context.Background(), id, readTestEpoch, now); err != nil || !changed {
		t.Fatalf("dispatch send: changed=%v err=%v", changed, err)
	}
	return attempt
}

func sendTestMessage(account *Account, ctx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
	prepared, err := account.PrepareSend(ctx, attempt)
	if err != nil {
		return model.SendAttemptResult{}, err
	}
	return prepared(ctx)
}

func TestSendingPreservesFixedSemanticsAndAcceptance(t *testing.T) {
	var attempt model.SendAttempt
	calls := 0
	account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch query := input.(type) {
		case *tg.MessagesSendMessageRequest:
			calls++
			if query.Message != attempt.Text || query.RandomID != attempt.RandomID {
				t.Fatal("message text or persisted random ID changed")
			}
			if !query.NoWebpage || query.ClearDraft || query.Silent || query.Background || query.Noforwards || query.AllowPaidFloodskip || query.AllowPaidStars != 0 || query.ScheduleDate != 0 || query.ReplyMarkup != nil || len(query.Entities) != 0 || query.ReplyTo != nil {
				t.Fatal("send enabled an unsupported message option")
			}
			if _, ok := query.Peer.(*tg.InputPeerSelf); !ok {
				t.Fatalf("wrong destination %T", query.Peer)
			}
			return encodeReadResponse(out, &tg.UpdateShortSentMessage{Out: true, ID: 23, Pts: 10, PtsCount: 0, Date: 100})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		default:
			t.Fatalf("unexpected send RPC %T", input)
			return errors.New("unexpected method")
		}
	})
	attempt = admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
	result, err := sendTestMessage(account, context.Background(), attempt)
	if err != nil || !result.Attempted || result.Receipt == nil {
		t.Fatalf("send acceptance lost: result=%+v err=%v", result, err)
	}
	if result.Receipt.MessageID.TelegramID() != 23 || result.Receipt.Peer != attempt.Peer || result.Receipt.RandomID != attempt.RandomID || result.Receipt.SentAt != time.Unix(100, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("incorrect receipt: %+v", result.Receipt)
	}
	operation, found, err := repository.Find(context.Background(), attempt.OperationID, time.Now())
	if err != nil || !found || operation.State != model.SendSent || operation.MessageID == nil || operation.MessageID.TelegramID() != 23 {
		t.Fatalf("acceptance was not persisted: found=%v operation=%+v err=%v", found, operation, err)
	}
	if calls != 1 {
		t.Fatalf("send calls=%d", calls)
	}
}

func TestSendingRecordsRawAcceptanceBeforeHookFailure(t *testing.T) {
	var account *Account
	account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch input.(type) {
		case *tg.MessagesSendMessageRequest:
			err := encodeReadResponse(out, &tg.UpdateShortSentMessage{Out: true, ID: 24, Pts: 11, PtsCount: 1, Date: 100})
			account.reads.fail(errors.New("synthetic update failure"))
			return err
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		default:
			return errors.New("unexpected method")
		}
	})
	attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
	result, err := sendTestMessage(account, context.Background(), attempt)
	if err == nil || result.Receipt == nil || result.Receipt.MessageID.TelegramID() != 24 || result.FailureCode != model.SendFailureStateSyncFailed {
		t.Fatalf("post-ack failure erased acceptance: result=%+v err=%v", result, err)
	}
	operation, found, findErr := repository.Find(context.Background(), attempt.OperationID, time.Now())
	if findErr != nil || !found || operation.State != model.SendSent {
		t.Fatalf("raw acknowledgment not durable: found=%v operation=%+v err=%v", found, operation, findErr)
	}
	if account.Ready() {
		t.Fatal("update failure left runtime ready for another send")
	}
}

func TestSendingRecordsAcceptanceAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch input.(type) {
		case *tg.MessagesSendMessageRequest:
			err := encodeReadResponse(out, &tg.UpdateShortSentMessage{Out: true, ID: 25, Pts: 10, Date: 100})
			cancel()
			return err
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		default:
			return errors.New("unexpected method")
		}
	})
	attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
	result, _ := sendTestMessage(account, ctx, attempt)
	if result.Receipt == nil || result.Receipt.MessageID.TelegramID() != 25 {
		t.Fatalf("cancelled response lost known receipt: %+v", result)
	}
	operation, found, err := repository.Find(context.Background(), attempt.OperationID, time.Now())
	if err != nil || !found || operation.State != model.SendSent {
		t.Fatalf("cancelled caller suppressed receipt persistence: found=%v state=%s err=%v", found, operation.State, err)
	}
}

func TestSendingReceiptFailurePreservesKnownAcceptanceAndFencesRuntime(t *testing.T) {
	account, repository, db := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch input.(type) {
		case *tg.MessagesSendMessageRequest:
			return encodeReadResponse(out, &tg.UpdateShortSentMessage{Out: true, ID: 26, Pts: 10, Date: 100})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		default:
			return errors.New("unexpected method")
		}
	})
	attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
	if _, err := db.Exec(`CREATE TRIGGER reject_outgoing_receipt BEFORE UPDATE ON outgoing_operations BEGIN SELECT RAISE(FAIL,'private synthetic persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := sendTestMessage(account, context.Background(), attempt)
	if err == nil || result.Receipt == nil || result.Receipt.MessageID.TelegramID() != 26 || result.FailureCode != model.SendFailureReceiptNotPersisted {
		t.Fatalf("receipt persistence failure disguised acceptance: result=%+v err=%v", result, err)
	}
	if strings.Contains(err.Error(), "private synthetic") {
		t.Fatal("private persistence diagnostic escaped adapter")
	}
	operation, found, findErr := repository.Find(context.Background(), attempt.OperationID, time.Now())
	if findErr != nil || !found || operation.State != model.SendInFlight {
		t.Fatalf("durable ambiguity was erased: found=%v state=%s err=%v", found, operation.State, findErr)
	}
	if account.Ready() {
		t.Fatal("receipt persistence failure left runtime ready")
	}
}

func TestSendingAmbiguousProviderErrorsNeverBecomeDefinitiveRejections(t *testing.T) {
	for _, providerError := range []*tgerr.Error{{Code: 500, Type: "INTERNAL"}, {Code: 400, Type: "RANDOM_ID_DUPLICATE"}} {
		t.Run(providerError.Type, func(t *testing.T) {
			calls := 0
			account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.MessagesSendMessageRequest:
					calls++
					return providerError
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				default:
					return errors.New("unexpected method")
				}
			})
			attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
			result, err := sendTestMessage(account, context.Background(), attempt)
			if err == nil || !result.Attempted || !result.Uncertain || result.Receipt != nil || result.FailureCode != model.SendFailureEffectUncertain {
				t.Fatalf("ambiguous provider result misclassified: result=%+v err=%v", result, err)
			}
			if calls != 1 {
				t.Fatalf("ambiguous request retried %d times", calls)
			}
		})
	}
}

func TestSendingForumRepliesValidateActualRouteAndUseNativeFallback(t *testing.T) {
	for _, variant := range []string{"topic root", "topic reply", "service root", "General reply", "deleted reply fallback", "wrong topic", "wrong dialog", "missing target"} {
		t.Run(variant, func(t *testing.T) {
			parent, _ := model.NewPeerID(model.PeerKindChannel, 8)
			topicID := int32(5)
			if variant == "General reply" {
				topicID = 1
			}
			peer, _ := model.NewTopicPeer(parent, topicID)
			var reply *model.MessageID
			if variant != "topic root" {
				replyID := int32(42)
				if variant == "service root" {
					replyID = topicID
				}
				id, _ := model.NewMessageID(peer, replyID)
				reply = &id
			}
			var message tg.MessageClass
			if reply != nil {
				message = &tg.Message{ID: int(reply.TelegramID()), PeerID: &tg.PeerChannel{ChannelID: 8}, FromID: &tg.PeerUser{UserID: 1}, Date: 100, Message: "incidental body must stay adapter-private"}
				if topicID != 1 {
					message.(*tg.Message).ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: int(topicID)}
				}
				switch variant {
				case "service root":
					message = &tg.MessageService{ID: int(topicID), PeerID: &tg.PeerChannel{ChannelID: 8}, Date: 100, Action: &tg.MessageActionTopicCreate{Title: "Synthetic topic"}}
				case "wrong topic":
					message.(*tg.Message).ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 9}
				case "wrong dialog":
					message.(*tg.Message).PeerID = &tg.PeerChannel{ChannelID: 99}
				case "missing target":
					message = nil
				}
			}
			channel := &tg.Channel{ID: 8, Megagroup: true, Forum: true, Noforwards: true, Title: "Synthetic forum", Photo: &tg.ChatPhotoEmpty{}}
			channel.SetAccessHash(7654321)
			sends, acks := 0, 0
			account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch query := input.(type) {
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{channel}})
				case *tg.MessagesGetForumTopicsByIDRequest:
					if len(query.Topics) != 1 || query.Topics[0] != int(topicID) {
						t.Fatal("preflight widened topic selection")
					}
					return encodeReadResponse(out, &tg.MessagesForumTopics{Pts: 10, Count: 1, Chats: []tg.ChatClass{channel}, Topics: []tg.ForumTopicClass{&tg.ForumTopic{ID: int(topicID), Peer: &tg.PeerChannel{ChannelID: 8}, FromID: &tg.PeerUser{UserID: 1}, Date: 100, TopMessage: 42, Title: "Synthetic topic"}}})
				case *tg.ChannelsGetMessagesRequest:
					if reply == nil || len(query.ID) != 1 {
						t.Fatal("reply lookup was not exact")
					}
					if selected, ok := query.ID[0].(*tg.InputMessageID); !ok || selected.ID != int(reply.TelegramID()) {
						t.Fatal("reply lookup requested another message")
					}
					messages := []tg.MessageClass{}
					if message != nil {
						messages = append(messages, message)
					}
					return encodeReadResponse(out, &tg.MessagesChannelMessages{Pts: 10, Messages: messages, Chats: []tg.ChatClass{channel}})
				case *tg.MessagesGetHistoryRequest:
					if reply == nil || query.OffsetID != int(reply.TelegramID()) || query.MinID != int(reply.TelegramID())-1 || query.MaxID != int(reply.TelegramID())+1 || query.Limit != 1 {
						t.Fatal("reply history lookup was not exact")
					}
					messages := []tg.MessageClass{}
					if message != nil {
						messages = append(messages, message)
					}
					return encodeReadResponse(out, &tg.MessagesChannelMessages{Pts: 10, Messages: messages, Chats: []tg.ChatClass{channel}})
				case *tg.MessagesSendMessageRequest:
					sends++
					if _, ok := query.SendAs.(*tg.InputPeerSelf); !ok {
						t.Fatal("forum send relied on default author")
					}
					routing, ok := query.ReplyTo.(*tg.InputReplyToMessage)
					if !ok {
						t.Fatal("topic routing missing")
					}
					wantedReply := int(topicID)
					if reply != nil {
						wantedReply = int(reply.TelegramID())
					}
					wantedTop := 0
					if topicID != 1 && wantedReply != int(topicID) {
						wantedTop = int(topicID)
					}
					if routing.ReplyToMsgID != wantedReply || routing.TopMsgID != wantedTop || routing.ReplyToPeerID != nil || routing.QuoteText != "" {
						t.Fatalf("incorrect forum routing: %+v", routing)
					}
					if variant == "deleted reply fallback" {
						accepted := &tg.Message{Out: true, ID: 50, PeerID: &tg.PeerChannel{ChannelID: 8}, FromID: &tg.PeerUser{UserID: 1}, Date: 100, Message: "Synthetic text", ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 1}}
						return encodeReadResponse(out, &tg.Updates{Date: 100, Seq: 1, Updates: []tg.UpdateClass{&tg.UpdateMessageID{RandomID: query.RandomID, ID: 50}, &tg.UpdateNewChannelMessage{Message: accepted, Pts: 10}}})
					}
					return encodeReadResponse(out, &tg.UpdateShortSentMessage{Out: true, ID: 50, Pts: 10, Date: 100})
				case *tg.MessagesReadHistoryRequest, *tg.ChannelsReadHistoryRequest, *tg.MessagesReadDiscussionRequest:
					acks++
					return errors.New("preflight cannot acknowledge history")
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				default:
					t.Fatalf("unexpected forum preflight RPC %T", input)
					return errors.New("unexpected method")
				}
			})
			if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 8, 7654321); err != nil {
				t.Fatal(err)
			}
			attempt := admitSendingTestAttempt(t, repository, peer, reply)
			result, err := sendTestMessage(account, context.Background(), attempt)
			invalid := variant == "wrong topic" || variant == "wrong dialog" || variant == "missing target"
			if invalid {
				if err == nil || result.Attempted || sends != 0 {
					t.Fatalf("invalid live route dispatched: sends=%d result=%+v err=%v", sends, result, err)
				}
			} else if err != nil || result.Receipt == nil || sends != 1 {
				t.Fatalf("valid topic route rejected: sends=%d result=%+v err=%v", sends, result, err)
			}
			if acks != 0 {
				t.Fatal("reply preflight acknowledged history")
			}
		})
	}
}

func TestSendingLiveMappingIsDurableBeforeSequenceCheckpoint(t *testing.T) {
	account, repository, db := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.UpdatesGetDifferenceRequest:
			return encodeReadResponse(out, &tg.UpdatesDifferenceEmpty{Date: 100, Seq: 1})
		default:
			return errors.New("unexpected method")
		}
	})
	attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
	if _, err := db.Exec(`CREATE TRIGGER require_outgoing_receipt BEFORE UPDATE OF seq ON telegram_update_state WHEN NEW.seq=2 AND NOT EXISTS(SELECT 1 FROM outgoing_operations WHERE state='sent') BEGIN SELECT RAISE(FAIL,'outgoing mapping must precede checkpoint'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- account.reads.manager.Run(ctx, account.reads, 1, updates.AuthOptions{OnStart: func(context.Context) { close(started) }})
	}()
	<-started
	defer func() { cancel(); <-done }()
	if err := account.reads.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{RandomID: attempt.RandomID, ID: 27}}, Date: 101, Seq: 2}); err != nil {
		t.Fatal(err)
	}
	if err := account.reads.waitCheckpoint(ctx, 10, 0, 2); err != nil {
		t.Fatal(err)
	}
	operation, found, err := repository.Find(context.Background(), attempt.OperationID, time.Now())
	if err != nil || !found || operation.State != model.SendSent || operation.MessageID == nil || operation.MessageID.TelegramID() != 27 || operation.SentAt != nil {
		t.Fatalf("mapping fact was lost or invented a date: found=%v operation=%+v err=%v", found, operation, err)
	}
}

func TestSendingDifferenceCapturesOnlyValidatedPositiveMappings(t *testing.T) {
	for _, invalidState := range []bool{false, true} {
		t.Run(map[bool]string{false: "validated", true: "invalid state"}[invalidState], func(t *testing.T) {
			var attempt model.SendAttempt
			account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.UpdatesGetDifferenceRequest:
					pts := 11
					if invalidState {
						pts = 9
					}
					return encodeReadResponse(out, &tg.UpdatesDifference{OtherUpdates: []tg.UpdateClass{&tg.UpdateMessageID{RandomID: attempt.RandomID, ID: 28}}, State: tg.UpdatesState{Pts: pts, Date: 101, Seq: 2}})
				default:
					return errors.New("unexpected method")
				}
			})
			attempt = admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
			_, err := account.reads.UpdatesGetDifference(context.Background(), &tg.UpdatesGetDifferenceRequest{Pts: 10, Date: 100})
			if invalidState && err == nil || !invalidState && err != nil {
				t.Fatalf("difference validation result=%v", err)
			}
			operation, found, findErr := repository.Find(context.Background(), attempt.OperationID, time.Now())
			if findErr != nil || !found {
				t.Fatalf("operation lookup: found=%v err=%v", found, findErr)
			}
			if invalidState && operation.State != model.SendInFlight || !invalidState && operation.State != model.SendSent {
				t.Fatalf("invalid evidence crossed recovery validation: state=%s", operation.State)
			}
		})
	}
}

func TestSendingInvocationMarkerPreservesLegacyUsernameWaiter(t *testing.T) {
	for _, outgoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "outgoing"}[outgoing], func(t *testing.T) {
			calls := 0
			waiter := outgoingWaiter{legacy: floodwait.NewSimpleWaiter().WithMaxWait(time.Second).WithMaxRetries(1)}
			invoke := waiter.Handle(gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				calls++
				return tgerr.New(420, "FLOOD_WAIT_1")
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if outgoing {
				ctx = context.WithValue(ctx, outgoingContextKey{}, &outgoingCall{})
			}
			err := invoke(ctx, &tg.ContactsResolveUsernameRequest{Username: "synthetic"}, &tg.ContactsResolvedPeer{})
			if outgoing {
				if _, ok := tgerr.As(err); !ok || ctx.Err() != nil {
					t.Fatalf("outgoing resolution entered legacy waiter: %v", err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("existing read stopped waiting: %v", err)
			}
			if calls != 1 {
				t.Fatalf("unexpected retry count %d", calls)
			}
		})
	}
}

func TestSendingFullUpdateReceiptsRejectConflictsAndWrongPeers(t *testing.T) {
	for _, variant := range []string{"updates", "combined", "conflicting mapping", "wrong peer"} {
		t.Run(variant, func(t *testing.T) {
			var attempt model.SendAttempt
			account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.MessagesSendMessageRequest:
					message := testMessage(29)
					items := []tg.UpdateClass{&tg.UpdateMessageID{RandomID: attempt.RandomID, ID: 29}, &tg.UpdateNewMessage{Message: message, Pts: 10}}
					if variant == "conflicting mapping" {
						items = append(items, &tg.UpdateMessageID{RandomID: attempt.RandomID, ID: 30})
					}
					if variant == "wrong peer" {
						message.PeerID = &tg.PeerUser{UserID: 99}
					}
					if variant == "combined" {
						return encodeReadResponse(out, &tg.UpdatesCombined{Updates: items, Date: 100, SeqStart: 1, Seq: 1})
					}
					return encodeReadResponse(out, &tg.Updates{Updates: items, Date: 100, Seq: 1})
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				default:
					return errors.New("unexpected method")
				}
			})
			attempt = admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
			result, err := sendTestMessage(account, context.Background(), attempt)
			valid := variant == "updates" || variant == "combined"
			if valid {
				if err != nil || result.Receipt == nil || result.Receipt.MessageID.TelegramID() != 29 {
					t.Fatalf("matching full receipt lost: result=%+v err=%v", result, err)
				}
			} else if err == nil || result.Receipt != nil || !result.Uncertain {
				t.Fatalf("invalid full receipt accepted: result=%+v err=%v", result, err)
			}
			operation, found, findErr := repository.Find(context.Background(), attempt.OperationID, time.Now())
			if findErr != nil || !found || valid && operation.State != model.SendSent || !valid && operation.State != model.SendInFlight {
				t.Fatalf("invalid mapping changed durable facts: found=%v state=%s err=%v", found, operation.State, findErr)
			}
		})
	}
}

func TestSendingProviderWaitIsImmediateOutgoingAndSharedWithLegacyRead(t *testing.T) {
	calls := 0
	invoke := gotdtelegram.InvokeFunc(func(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
		if _, ok := input.(*tg.ContactsResolveUsernameRequest); !ok {
			t.Fatalf("unexpected wait RPC %T", input)
		}
		calls++
		return tgerr.New(420, "FLOOD_WAIT_3")
	})
	account, repository, _ := newSendingTestAccount(t, invoke)
	account.reads.api = tg.NewClient(outgoingWaiter{legacy: floodwait.NewSimpleWaiter().WithMaxWait(5 * time.Second).WithMaxRetries(3)}.Handle(rpcControl{runtime: account.reads, environment: account.environment}.Handle(readMiddleware{account.reads}.Handle(invoke))))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := account.ResolveRecipient(ctx, model.ResolveRecipientQuery{Username: "synthetic"})
	var sendError *model.SendError
	if !errors.As(err, &sendError) || sendError.FailureCode != model.SendFailureRateLimited || sendError.RetryAt.IsZero() || time.Since(started) > 200*time.Millisecond || ctx.Err() != nil {
		t.Fatalf("outgoing wait was hidden by a retry: %v", err)
	}
	before, err := repository.WaitUntil(context.Background(), account.environment, 1, "contacts.resolveUsername", model.PeerID{}, time.Now())
	if err != nil || !before.Equal(sendError.RetryAt) {
		t.Fatalf("provider deadline not retained exactly: before=%v retry=%v err=%v", before, sendError.RetryAt, err)
	}
	readCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	_, err = account.ResolveMessagePublisher(readCtx, "synthetic")
	if err == nil || readCtx.Err() == nil || calls != 1 {
		t.Fatalf("legacy read bypassed common wait: calls=%d err=%v", calls, err)
	}
	after, err := repository.WaitUntil(context.Background(), account.environment, 1, "contacts.resolveUsername", model.PeerID{}, time.Now())
	if err != nil || !after.Equal(before) {
		t.Fatalf("cancelled read stretched provider deadline: before=%v after=%v err=%v", before, after, err)
	}
}

func TestSendingSelectsExplicitGroupAndPublisherAuthors(t *testing.T) {
	for _, kind := range []model.PeerKind{model.PeerKindChat, model.PeerKindChannel} {
		t.Run(string(kind), func(t *testing.T) {
			peer, _ := model.NewPeerID(kind, 8)
			channel := &tg.Channel{ID: 8, Broadcast: true, Creator: true, Noforwards: true, Title: "Synthetic publisher", Photo: &tg.ChatPhotoEmpty{}}
			channel.SetAccessHash(7654321)
			account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch query := input.(type) {
				case *tg.MessagesGetChatsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{&tg.Chat{ID: 8, Noforwards: true, Title: "Synthetic group", Photo: &tg.ChatPhotoEmpty{}}}})
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{channel}})
				case *tg.MessagesSendMessageRequest:
					if kind == model.PeerKindChat {
						if _, ok := query.SendAs.(*tg.InputPeerSelf); !ok {
							t.Fatal("basic group relied on default author")
						}
					} else if author, ok := query.SendAs.(*tg.InputPeerChannel); !ok || author.ChannelID != peer.TelegramID() {
						t.Fatal("broadcast did not select destination publisher")
					}
					return encodeReadResponse(out, &tg.UpdateShortSentMessage{Out: true, ID: 31, Pts: 10, Date: 100})
				default:
					return errors.New("unexpected method")
				}
			})
			if kind == model.PeerKindChannel {
				if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 8, 7654321); err != nil {
					t.Fatal(err)
				}
			}
			attempt := admitSendingTestAttempt(t, repository, peer, nil)
			result, err := sendTestMessage(account, context.Background(), attempt)
			if err != nil || result.Receipt == nil {
				t.Fatalf("protected destination rejected new text: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSendingOrdinarySupergroupThreadsPreserveReplyRoute(t *testing.T) {
	for _, variant := range []string{"ordinary thread", "external quote", "unexpected forum route"} {
		t.Run(variant, func(t *testing.T) {
			forumHeader := variant == "unexpected forum route"
			peer, _ := model.NewPeerID(model.PeerKindChannel, 8)
			reply, _ := model.NewMessageID(peer, 44)
			channel := &tg.Channel{ID: 8, Megagroup: true, Title: "Synthetic supergroup", Photo: &tg.ChatPhotoEmpty{}}
			channel.SetAccessHash(7654321)
			header := &tg.MessageReplyHeader{ForumTopic: forumHeader, ReplyToMsgID: 43}
			header.SetReplyToTopID(42)
			if variant == "external quote" {
				header.Quote = true
				header.SetReplyToPeerID(&tg.PeerChannel{ChannelID: 99})
				header.SetQuoteText("external origin remains unrequested")
			}
			message := &tg.Message{ID: 44, PeerID: &tg.PeerChannel{ChannelID: 8}, Date: 100, Message: "incidental thread content", ReplyTo: header}
			sends := 0
			account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch query := input.(type) {
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{channel}})
				case *tg.ChannelsGetMessagesRequest:
					if len(query.ID) != 1 {
						t.Fatal("thread reply lookup widened its selection")
					}
					selected, ok := query.ID[0].(*tg.InputMessageID)
					if !ok || selected.ID != 44 {
						t.Fatal("thread reply lookup selected another message")
					}
					return encodeReadResponse(out, &tg.MessagesChannelMessages{Pts: 10, Messages: []tg.MessageClass{message}, Chats: []tg.ChatClass{channel}})
				case *tg.MessagesSendMessageRequest:
					sends++
					routing, ok := query.ReplyTo.(*tg.InputReplyToMessage)
					if !ok || routing.ReplyToMsgID != 44 || routing.TopMsgID != 0 || routing.ReplyToPeerID != nil {
						t.Fatal("ordinary thread reply changed native routing")
					}
					accepted := &tg.Message{Out: true, ID: 45, PeerID: &tg.PeerChannel{ChannelID: 8}, FromID: &tg.PeerUser{UserID: 1}, Date: 100, Message: query.Message, ReplyTo: header}
					return encodeReadResponse(out, &tg.Updates{Date: 100, Seq: 1, Updates: []tg.UpdateClass{&tg.UpdateMessageID{RandomID: query.RandomID, ID: 45}, &tg.UpdateNewChannelMessage{Message: accepted, Pts: 10}}})
				default:
					t.Fatalf("unexpected ordinary thread RPC %T", input)
					return errors.New("unexpected method")
				}
			})
			if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 8, 7654321); err != nil {
				t.Fatal(err)
			}
			attempt := admitSendingTestAttempt(t, repository, peer, &reply)
			result, err := sendTestMessage(account, context.Background(), attempt)
			if forumHeader {
				if err == nil || result.Attempted || sends != 0 {
					t.Fatalf("unexpected forum route dispatched: sends=%d result=%+v err=%v", sends, result, err)
				}
			} else if err != nil || sends != 1 || result.Receipt == nil || result.Receipt.MessageID.TelegramID() != 45 {
				t.Fatalf("ordinary thread rejected: sends=%d result=%+v err=%v", sends, result, err)
			}
		})
	}
}

func TestSendingForumQuotedTargetsRequireLocalTopicHeader(t *testing.T) {
	parent, _ := model.NewPeerID(model.PeerKindChannel, 8)
	peer, _ := model.NewTopicPeer(parent, 5)
	for _, localTop := range []bool{false, true} {
		header := &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 5, Quote: true}
		header.SetReplyToPeerID(&tg.PeerChannel{ChannelID: 99})
		if localTop {
			header.SetReplyToTopID(5)
		}
		if got := sendReplyRoute(peer, 44, header, false); got != localTop {
			t.Fatalf("quoted target route inferred from external origin: localTop=%v accepted=%v", localTop, got)
		}
	}
}

func TestSendingContradictoryAcknowledgmentPreservesDurableReceipt(t *testing.T) {
	var repository *store.OutgoingRepository
	account, outgoing, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		query, ok := input.(*tg.MessagesSendMessageRequest)
		if !ok {
			t.Fatalf("unexpected acknowledgment RPC %T", input)
		}
		if _, found, err := repository.RecordReceipt(context.Background(), readTestEpoch, query.RandomID, 71, nil, time.Now().UTC()); err != nil || !found {
			t.Fatalf("earlier mapping: found=%v err=%v", found, err)
		}
		return encodeReadResponse(out, &tg.UpdateShortSentMessage{Out: true, ID: 72, Pts: 10, Date: 100})
	})
	repository = outgoing
	attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
	result, err := sendTestMessage(account, context.Background(), attempt)
	if err == nil || result.Receipt == nil || result.Receipt.MessageID.TelegramID() != 71 || result.FailureCode != model.SendFailureReceiptNotPersisted || account.Ready() {
		t.Fatalf("contradictory acknowledgment replaced known receipt: result=%+v err=%v", result, err)
	}
	operation, found, err := repository.Find(context.Background(), attempt.OperationID, time.Now())
	if err != nil || !found || operation.MessageID == nil || operation.MessageID.TelegramID() != 71 {
		t.Fatalf("durable receipt changed: operation=%+v found=%v err=%v", operation, found, err)
	}
}

func TestSendingAttemptedRejectionsRemainUncertain(t *testing.T) {
	for _, test := range []struct {
		provider string
		code     model.SendFailureCode
	}{
		{"CHAT_WRITE_FORBIDDEN", model.SendFailureWriteForbidden},
		{"ALLOW_PAYMENT_REQUIRED_1", model.SendFailurePaymentRequired},
		{"PEER_FLOOD", model.SendFailureAccountRestricted},
		{"MESSAGE_TOO_LONG", model.SendFailureMessageTooLong},
		{"FLOOD_WAIT_3", model.SendFailureRateLimited},
	} {
		t.Run(test.provider, func(t *testing.T) {
			var seenRandomID int64
			invoke := gotdtelegram.InvokeFunc(func(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
				query, ok := input.(*tg.MessagesSendMessageRequest)
				if !ok {
					t.Fatalf("unexpected send RPC %T", input)
				}
				seenRandomID = query.RandomID
				return tgerr.New(400, test.provider)
			})
			account, repository, _ := newSendingTestAccount(t, invoke)
			account.reads.api = tg.NewClient(rpcControl{runtime: account.reads, environment: account.environment}.Handle(readMiddleware{account.reads}.Handle(invoke)))
			attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
			result, err := sendTestMessage(account, context.Background(), attempt)
			if err == nil || !result.Attempted || !result.Uncertain || result.Receipt != nil || result.FailureCode != test.code || seenRandomID != attempt.RandomID {
				t.Fatalf("opaque SDK history was treated as no effect: result=%+v random=%d err=%v", result, seenRandomID, err)
			}
			if test.code == model.SendFailureRateLimited && result.RetryAt.IsZero() {
				t.Fatal("known provider retry deadline was discarded")
			}
		})
	}
}

func TestSendingChannelShortAcknowledgmentNeverAdvancesCommonPTS(t *testing.T) {
	peer, _ := model.NewPeerID(model.PeerKindChannel, 8)
	channel := &tg.Channel{ID: 8, Broadcast: true, Creator: true, Title: "Synthetic publisher", Photo: &tg.ChatPhotoEmpty{}}
	channel.SetAccessHash(7654321)
	differences := 0
	account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch input.(type) {
		case *tg.ChannelsGetChannelsRequest:
			return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{channel}})
		case *tg.MessagesSendMessageRequest:
			return encodeReadResponse(out, &tg.UpdateShortSentMessage{Out: true, ID: 73, Pts: 9000, PtsCount: 1, Date: 101})
		case *tg.UpdatesGetDifferenceRequest:
			differences++
			if differences != 1 {
				return errors.New("channel pts triggered common recovery")
			}
			return encodeReadResponse(out, &tg.UpdatesDifferenceEmpty{Date: 100, Seq: 1})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		default:
			return errors.New("unexpected method")
		}
	})
	if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 8, 7654321); err != nil {
		t.Fatal(err)
	}
	attempt := admitSendingTestAttempt(t, repository, peer, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- account.reads.manager.Run(ctx, account.reads, 1, updates.AuthOptions{OnStart: func(context.Context) { close(started) }})
	}()
	<-started
	defer func() { cancel(); <-done }()
	result, err := sendTestMessage(account, ctx, attempt)
	if err != nil || result.Receipt == nil {
		t.Fatalf("channel acknowledgment: result=%+v err=%v", result, err)
	}
	if err := account.reads.Handle(ctx, &tg.Updates{Date: 102, Seq: 2}); err != nil {
		t.Fatal(err)
	}
	if err := account.reads.waitCheckpoint(ctx, 10, 0, 2); err != nil {
		t.Fatal(err)
	}
	state, found, err := account.reads.storage.GetState(ctx, 1)
	if err != nil || !found || state.Pts != 10 {
		t.Fatalf("channel pts contaminated common checkpoint: state=%+v err=%v", state, err)
	}
}

func TestSendingWaitPersistenceFailureRetainsMetadataGraceMarker(t *testing.T) {
	for _, duringSend := range []bool{false, true} {
		t.Run(map[bool]string{false: "resolution", true: "send"}[duringSend], func(t *testing.T) {
			invoke := gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				return tgerr.New(420, "FLOOD_WAIT_3")
			})
			account, repository, database := newSendingTestAccount(t, invoke)
			account.reads.api = tg.NewClient(rpcControl{runtime: account.reads, environment: account.environment}.Handle(readMiddleware{account.reads}.Handle(invoke)))
			if _, err := database.Exec(`CREATE TRIGGER reject_provider_wait BEFORE INSERT ON telegram_rpc_waits BEGIN SELECT RAISE(FAIL,'private wait persistence failure'); END`); err != nil {
				t.Fatal(err)
			}
			var err error
			if duringSend {
				attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
				var result model.SendAttemptResult
				result, err = sendTestMessage(account, context.Background(), attempt)
				if !result.Attempted || !result.Uncertain || result.Receipt != nil {
					t.Fatalf("failed wait fact changed send certainty: %+v", result)
				}
			} else {
				_, err = account.ResolveRecipient(context.Background(), model.ResolveRecipientQuery{Username: "synthetic"})
			}
			if !errors.Is(err, model.ErrSendMetadataRecording) || strings.Contains(err.Error(), "private wait") || account.Ready() {
				t.Fatalf("metadata grace marker lost or private cause exposed: ready=%v err=%v", account.Ready(), err)
			}
		})
	}
}

func TestSendingReceiptContextsReuseOneCancellationIndependentDeadline(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	stop()
	capture := &sendCapture{}
	first, cancelFirst := receiptRecordingContext(parent, capture)
	defer cancelFirst()
	deadline, ok := first.Deadline()
	if !ok || first.Err() != nil || deadline.After(time.Now().Add(receiptGrace)) {
		t.Fatal("receipt recording did not start a bounded independent grace")
	}
	second, cancelSecond := receiptRecordingContext(parent, capture)
	defer cancelSecond()
	secondDeadline, ok := second.Deadline()
	if !ok || second.Err() != nil || !secondDeadline.Equal(deadline) {
		t.Fatal("another receipt fact restarted the local recording grace")
	}
}

func TestSendingRecordedProviderWaitCarriesOriginalMetadataDeadline(t *testing.T) {
	invoke := gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		return tgerr.New(420, "FLOOD_WAIT_3")
	})
	account, _, _ := newSendingTestAccount(t, invoke)
	deadline := time.Now().Add(time.Second)
	capture := &sendCapture{recordingDeadline: deadline}
	ctx := context.WithValue(context.Background(), outgoingContextKey{}, &outgoingCall{peer: testSelfPeer(t), capture: capture})
	err := (rpcControl{runtime: account.reads, environment: account.environment}).Handle(readMiddleware{account.reads}.Handle(invoke)).Invoke(ctx, &tg.MessagesSendMessageRequest{}, &tg.UpdatesBox{})
	var failure *model.SendError
	if !errors.As(err, &failure) || failure.FailureCode != model.SendFailureRateLimited || !failure.MetadataDeadline.Equal(deadline) {
		t.Fatalf("recorded wait reset the remaining metadata budget: err=%v failure=%+v", err, failure)
	}
}

func TestSendingRemainingBatchMappingsCannotRestartReceiptGrace(t *testing.T) {
	account, repository, _ := newSendingTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("receipt recording must remain local")
		return nil
	})
	attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
	now := time.Now().UTC()
	secondID, err := model.NewOperationID(now.Add(time.Millisecond), bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	second := store.OutgoingOperation{OperationID: secondID, AuthorizationEpoch: readTestEpoch, Peer: attempt.Peer, RandomID: attempt.RandomID + 1, PayloadDigest: [32]byte{2}}
	if _, _, err := repository.Admit(context.Background(), second, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.BeginDispatch(context.Background(), secondID, readTestEpoch, now); err != nil {
		t.Fatal(err)
	}
	capture := &sendCapture{attempt: attempt}
	ctx := context.WithValue(context.Background(), outgoingContextKey{}, &outgoingCall{peer: attempt.Peer, capture: capture})
	firstMapping := &tg.UpdateMessageID{RandomID: attempt.RandomID, ID: 74}
	if err := account.reads.captureAcknowledgment(ctx, capture, &tg.Updates{Updates: []tg.UpdateClass{firstMapping}}); err != nil {
		t.Fatal(err)
	}
	if capture.recordingDeadline.IsZero() || capture.receipt == nil {
		t.Fatal("initial acknowledgment did not establish a receipt grace")
	}
	// Advancing the stored deadline past expiry models time consumed by the
	// first local write, without introducing a five-second test delay.
	expired := time.Now().Add(-time.Second)
	capture.recordingDeadline = expired
	err = account.reads.captureMappings(ctx, []tg.UpdateClass{firstMapping, &tg.UpdateMessageID{RandomID: second.RandomID, ID: 75}})
	if err == nil || !capture.recordingDeadline.Equal(expired) {
		t.Fatal("remaining mappings acquired a second receipt grace")
	}
	retained, found, err := repository.Find(context.Background(), secondID, time.Now())
	if err != nil || !found || retained.State != model.SendInFlight {
		t.Fatalf("expired grace persisted a later batch fact: found=%v state=%s err=%v", found, retained.State, err)
	}
}

func TestSendingUnrelatedAcknowledgmentFactsPreserveBudgetWithoutCurrentReceipt(t *testing.T) {
	for _, rejectWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded other fact", true: "failed other fact"}[rejectWrite], func(t *testing.T) {
			var otherRandomID int64
			var capture *sendCapture
			account, repository, database := newSendingTestAccount(t, func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
				if _, ok := input.(*tg.MessagesSendMessageRequest); !ok {
					t.Fatalf("unexpected acknowledgment RPC %T", input)
				}
				capture = ctx.Value(outgoingContextKey{}).(*outgoingCall).capture
				return encodeReadResponse(out, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{RandomID: otherRandomID, ID: 81}}, Date: 100, Seq: 1})
			})
			attempt := admitSendingTestAttempt(t, repository, testSelfPeer(t), nil)
			now := time.Now().UTC()
			otherID, err := model.NewOperationID(now.Add(time.Millisecond), bytes.NewReader(make([]byte, 32)))
			if err != nil {
				t.Fatal(err)
			}
			otherRandomID = attempt.RandomID + 1
			if _, _, err := repository.Admit(context.Background(), store.OutgoingOperation{OperationID: otherID, AuthorizationEpoch: readTestEpoch, Peer: attempt.Peer, RandomID: otherRandomID, PayloadDigest: [32]byte{2}}, now); err != nil {
				t.Fatal(err)
			}
			if _, _, err := repository.BeginDispatch(context.Background(), otherID, readTestEpoch, now); err != nil {
				t.Fatal(err)
			}
			if rejectWrite {
				if _, err := database.Exec(`CREATE TRIGGER reject_unrelated_receipt BEFORE UPDATE ON outgoing_operations BEGIN SELECT RAISE(FAIL,'private unrelated receipt failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			result, err := sendTestMessage(account, context.Background(), attempt)
			var failure *model.SendError
			if err == nil || !errors.As(err, &failure) || result.Receipt != nil || !result.Attempted || !result.Uncertain || capture == nil || capture.recordingDeadline.IsZero() || !failure.MetadataDeadline.Equal(capture.recordingDeadline) {
				t.Fatalf("unrelated fact changed current acceptance or recording budget: result=%+v err=%v failure=%+v", result, err, failure)
			}
			if errors.Is(err, model.ErrSendMetadataRecording) != rejectWrite || strings.Contains(err.Error(), "private unrelated") {
				t.Fatalf("unrelated fact failure marker lost or private cause exposed: %v", err)
			}
			current, found, findErr := repository.Find(context.Background(), attempt.OperationID, time.Now())
			if findErr != nil || !found || current.State != model.SendInFlight {
				t.Fatalf("current operation gained an unrelated receipt: found=%v state=%s err=%v", found, current.State, findErr)
			}
			other, found, findErr := repository.Find(context.Background(), otherID, time.Now())
			expected := model.SendSent
			if rejectWrite {
				expected = model.SendInFlight
			}
			if findErr != nil || !found || other.State != expected {
				t.Fatalf("other operation recording: found=%v state=%s want=%s err=%v", found, other.State, expected, findErr)
			}
		})
	}
}
