package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestMessagePublisherResolution(t *testing.T) {
	for _, mode := range []string{"valid", "alias", "forum", "left", "protected", "mismatch", "wrong_peer", "user", "oversized", "upstream"} {
		t.Run(mode, func(t *testing.T) {
			channel := syntheticSupergroup()
			channel.Username = "synthetic_chat"
			switch mode {
			case "alias":
				channel.Username = ""
				channel.Usernames = []tg.Username{{Username: "synthetic_chat", Active: true}}
			case "forum":
				channel.Forum = true
			case "left":
				channel.Left = true
			case "protected":
				channel.Noforwards = true
			case "mismatch":
				channel.Username = "different"
			}
			calls := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch q := input.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.ContactsResolveUsernameRequest:
					calls++
					if q.Username != "synthetic_chat" {
						t.Fatal("wrong username")
					}
					if mode == "upstream" {
						return errors.New("synthetic failure")
					}
					response := &tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 42}, Chats: []tg.ChatClass{channel}}
					if mode == "wrong_peer" {
						response.Peer = &tg.PeerChannel{ChannelID: 99}
					}
					if mode == "user" {
						response.Peer = &tg.PeerUser{UserID: 1}
						response.Chats = nil
						response.Users = []tg.UserClass{&tg.User{ID: 1}}
					}
					if mode == "oversized" {
						response.Chats = append(response.Chats, channel)
					}
					return encodeReadResponse(out, response)
				default:
					t.Fatalf("unexpected RPC %T", input)
					return nil
				}
			})
			result, err := account.ResolveMessagePublisher(context.Background(), "synthetic_chat")
			valid := mode == "valid" || mode == "alias" || mode == "forum"
			if valid {
				if err != nil || result.ID.TelegramID() != 42 || result.Forum != (mode == "forum") || calls != 1 {
					t.Fatal("valid resolution", err)
				}
			} else if err == nil || result.ID.String() != "" {
				t.Fatal("invalid resolution escaped", err)
			}
		})
	}
}
