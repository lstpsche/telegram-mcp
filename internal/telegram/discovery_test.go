package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestSavedReferenceDiscoveryIsExactAndContentFree(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "extra", "crossdialog", "crossmessage", "wrongid", "service", "invalidstate", "upstream", "syncfailure"} {
		t.Run(mode, func(t *testing.T) {
			lookups := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
				switch q := input.(type) {
				case *tg.MessagesGetPeerDialogsRequest:
					lookups++
					if len(q.Peers) != 1 {
						t.Fatal("discovery requested other dialogs")
					}
					peer, ok := q.Peers[0].(*tg.InputDialogPeer)
					if !ok {
						t.Fatal("unexpected dialog constructor")
					}
					if _, ok := peer.Peer.(*tg.InputPeerSelf); !ok {
						t.Fatal("discovery did not target self")
					}
					if mode == "upstream" {
						return errors.New("synthetic upstream failure")
					}
					message := testMessage(20)
					message.Message = "private incidental text"
					dialog := &tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, TopMessage: 20}
					result := &tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{dialog}, Messages: []tg.MessageClass{message}, State: tg.UpdatesState{Pts: 10, Date: 100, Seq: 1}}
					switch mode {
					case "missing":
						result.Messages = nil
					case "extra":
						result.Messages = append(result.Messages, testMessage(19))
					case "crossdialog":
						dialog.Peer = &tg.PeerUser{UserID: 2}
					case "crossmessage":
						message.PeerID = &tg.PeerUser{UserID: 2}
					case "wrongid":
						message.ID = 19
					case "service":
						result.Messages[0] = &tg.MessageService{ID: 20, PeerID: &tg.PeerUser{UserID: 1}, Action: &tg.MessageActionEmpty{}}
					case "invalidstate":
						result.State.Date = 0
					}
					return encodeReadResponse(output, result)
				case *tg.UpdatesGetStateRequest:
					if mode == "syncfailure" {
						return errors.New("synthetic sync failure")
					}
					return encodeReadResponse(output, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				default:
					t.Errorf("unexpected RPC: %T", input)
					return errors.New("unexpected RPC")
				}
			})
			id, err := account.latestSavedMessage(context.Background())
			if lookups != 1 {
				t.Fatal("unexpected lookup count", lookups)
			}
			if mode == "valid" {
				if err != nil || id.String() != "tgmsg:v1:self:1:20" {
					t.Fatal("reference discovery failed", err)
				}
			} else if err == nil || id.String() != "" {
				t.Fatal("unsafe reference was released", err)
			}
		})
	}
}

func TestSavedReferenceDiscoveryRequiresReadyRuntime(t *testing.T) {
	account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("unready discovery performed Telegram I/O")
		return nil
	})
	account.reads.ready.Store(false)
	if id, err := account.latestSavedMessage(context.Background()); err == nil || id.String() != "" {
		t.Fatal("unready discovery succeeded")
	}
}
