package telegram

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func TestSendingPreflightUsesConclusiveNativePostingRights(t *testing.T) {
	foreverPlain := &tg.ChatBannedRights{SendPlain: true}
	foreverMessages := &tg.ChatBannedRights{SendMessages: true, UntilDate: math.MaxInt32}
	expiredPlain := &tg.ChatBannedRights{SendPlain: true, UntilDate: int(time.Now().Add(-time.Hour).Unix())}
	activePlain := &tg.ChatBannedRights{SendPlain: true, UntilDate: int(time.Now().Add(time.Hour).Unix())}
	mediaOnly := &tg.ChatBannedRights{SendMedia: true, EmbedLinks: true}
	for _, test := range []struct {
		name          string
		basic         bool
		broadcast     bool
		forum         bool
		creator       bool
		admin         *tg.ChatAdminRights
		userBan       *tg.ChatBannedRights
		defaultBan    *tg.ChatBannedRights
		closed        bool
		myTopic       bool
		wantForbidden bool
	}{
		{name: "broadcast subscriber", broadcast: true, wantForbidden: true},
		{name: "broadcast admin without post right", broadcast: true, admin: &tg.ChatAdminRights{DeleteMessages: true}, wantForbidden: true},
		{name: "broadcast publisher", broadcast: true, admin: &tg.ChatAdminRights{PostMessages: true}},
		{name: "broadcast publisher with anonymous flag", broadcast: true, admin: &tg.ChatAdminRights{PostMessages: true, Anonymous: true}},
		{name: "broadcast creator", broadcast: true, creator: true},
		{name: "basic ordinary member", basic: true},
		{name: "basic default plain ban", basic: true, defaultBan: foreverPlain, wantForbidden: true},
		{name: "basic default message ban", basic: true, defaultBan: foreverMessages, wantForbidden: true},
		{name: "basic default active ban", basic: true, defaultBan: activePlain, wantForbidden: true},
		{name: "basic default expired ban", basic: true, defaultBan: expiredPlain},
		{name: "basic unrelated restrictions", basic: true, defaultBan: mediaOnly},
		{name: "basic admin exemption", basic: true, admin: &tg.ChatAdminRights{Other: true}, defaultBan: foreverPlain},
		{name: "basic creator exemption", basic: true, creator: true, defaultBan: foreverPlain},
		{name: "supergroup ordinary member"},
		{name: "supergroup individual plain ban", userBan: foreverPlain, wantForbidden: true},
		{name: "supergroup individually banned member", userBan: &tg.ChatBannedRights{ViewMessages: true}, wantForbidden: true},
		{name: "supergroup negative permanent ban", userBan: &tg.ChatBannedRights{SendPlain: true, UntilDate: -1}, wantForbidden: true},
		{name: "supergroup individual message ban", userBan: foreverMessages, wantForbidden: true},
		{name: "supergroup individual active ban", userBan: activePlain, wantForbidden: true},
		{name: "supergroup individual expired ban", userBan: expiredPlain},
		{name: "supergroup unrelated restrictions", userBan: mediaOnly},
		{name: "supergroup default boost exemption unknown", defaultBan: foreverPlain},
		{name: "supergroup individual ban despite possible boost exemption", userBan: foreverPlain, defaultBan: foreverPlain, wantForbidden: true},
		{name: "supergroup admin exemption", admin: &tg.ChatAdminRights{Other: true}, userBan: foreverPlain, defaultBan: foreverPlain},
		{name: "supergroup creator exemption", creator: true, userBan: foreverPlain, defaultBan: foreverPlain},
		{name: "supergroup anonymous admin cannot post as self", admin: &tg.ChatAdminRights{Other: true, Anonymous: true}, wantForbidden: true},
		{name: "supergroup anonymous creator cannot post as self", creator: true, admin: &tg.ChatAdminRights{Anonymous: true}, wantForbidden: true},
		{name: "open forum topic", forum: true},
		{name: "closed forum topic ordinary member", forum: true, closed: true, wantForbidden: true},
		{name: "closed forum topic unrelated admin", forum: true, closed: true, admin: &tg.ChatAdminRights{DeleteMessages: true}, wantForbidden: true},
		{name: "closed forum topic manager", forum: true, closed: true, admin: &tg.ChatAdminRights{ManageTopics: true}},
		{name: "closed forum topic anonymous manager cannot post as self", forum: true, closed: true, admin: &tg.ChatAdminRights{ManageTopics: true, Anonymous: true}, wantForbidden: true},
		{name: "closed forum topic group creator", forum: true, closed: true, creator: true},
		{name: "closed forum topic author", forum: true, closed: true, myTopic: true},
		{name: "closed forum topic author with individual ban", forum: true, closed: true, myTopic: true, userBan: foreverPlain, wantForbidden: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			kind := model.PeerKindChannel
			if test.basic {
				kind = model.PeerKindChat
			}
			peer, _ := model.NewPeerID(kind, 8)
			if test.forum {
				peer, _ = model.NewTopicPeer(peer, 5)
			}
			chat := &tg.Chat{ID: 8, Creator: test.creator, Noforwards: true, Title: "Synthetic group", Photo: &tg.ChatPhotoEmpty{}}
			channel := &tg.Channel{ID: 8, Creator: test.creator, Megagroup: !test.broadcast, Broadcast: test.broadcast, Forum: test.forum, Noforwards: true, Title: "Synthetic group", Photo: &tg.ChatPhotoEmpty{}}
			channel.SetAccessHash(7654321)
			if test.admin != nil {
				chat.SetAdminRights(*test.admin)
				channel.SetAdminRights(*test.admin)
			}
			if test.defaultBan != nil {
				chat.SetDefaultBannedRights(*test.defaultBan)
				channel.SetDefaultBannedRights(*test.defaultBan)
			}
			if test.userBan != nil {
				channel.SetBannedRights(*test.userBan)
			}
			sends := 0
			account, repository, _ := newSendingTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.MessagesGetChatsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{chat}})
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{channel}})
				case *tg.MessagesGetForumTopicsByIDRequest:
					topic := &tg.ForumTopic{ID: 5, Peer: &tg.PeerChannel{ChannelID: 8}, FromID: &tg.PeerUser{UserID: 2}, Date: 100, TopMessage: 42, Closed: test.closed, My: test.myTopic, Title: "Synthetic topic"}
					return encodeReadResponse(out, &tg.MessagesForumTopics{Pts: 10, Count: 1, Chats: []tg.ChatClass{channel}, Topics: []tg.ForumTopicClass{topic}})
				case *tg.MessagesSendMessageRequest:
					sends++
					return errors.New("unexpected irreversible RPC")
				default:
					t.Fatalf("posting preflight added an unexpected RPC %T", input)
					return errors.New("unexpected method")
				}
			})
			if !test.basic {
				if err := account.reads.storage.SetChannelAccessHash(ctx, 1, 8, 7654321); err != nil {
					t.Fatal(err)
				}
			}
			resolved, err := account.ResolveRecipient(ctx, model.ResolveRecipientQuery{Peer: peer})
			if err != nil || resolved.Peer != peer {
				t.Fatalf("read-only recipient resolution depended on posting rights: result=%+v err=%v", resolved, err)
			}
			now := time.Now().UTC()
			operationID, err := model.NewOperationID(now, bytes.NewReader(make([]byte, 16)))
			if err != nil {
				t.Fatal(err)
			}
			attempt := model.SendAttempt{OperationID: operationID, Peer: peer, Text: "Synthetic text", RandomID: 987654321}
			if _, admitted, err := repository.Admit(ctx, store.OutgoingOperation{OperationID: operationID, AuthorizationEpoch: readTestEpoch, Peer: peer, RandomID: attempt.RandomID, PayloadDigest: [32]byte{1}}, now); err != nil || !admitted {
				t.Fatal("admit operation", admitted, err)
			}
			prepared, err := account.PrepareSend(ctx, attempt)
			if test.wantForbidden {
				var failure *model.SendError
				if !errors.As(err, &failure) || failure.Category != model.ErrorWriteForbidden || failure.FailureCode != model.SendFailureWriteForbidden || prepared != nil {
					t.Fatalf("known posting restriction was not rejected before dispatch: prepared=%v err=%v", prepared != nil, err)
				}
			} else if err != nil || prepared == nil {
				t.Fatalf("posting preflight over-restricted a supported destination: prepared=%v err=%v", prepared != nil, err)
			}
			operation, exists, err := repository.Find(ctx, operationID, time.Now())
			if err != nil || !exists || operation.State != model.SendNotSent || sends != 0 {
				t.Fatalf("preflight changed dispatch state or invoked a send: state=%s exists=%v sends=%d err=%v", operation.State, exists, sends, err)
			}
		})
	}
}
