package telegram

import (
	"context"
	"reflect"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestMediaSearchUsesProviderFiltersWithoutDownloads(t *testing.T) {
	for _, spec := range []struct {
		kind    model.SearchMediaType
		filter  tg.MessagesFilterClass
		message func() *tg.Message
	}{
		{model.SearchMediaPhoto, &tg.InputMessagesFilterPhotos{}, testPhotoMessage},
		{model.SearchMediaImageFile, &tg.InputMessagesFilterDocument{}, testDocumentMessage},
		{model.SearchMediaPDF, &tg.InputMessagesFilterDocument{}, func() *tg.Message { return attachmentMessage("application/pdf") }},
		{model.SearchMediaTextFile, &tg.InputMessagesFilterDocument{}, func() *tg.Message { return attachmentMessage("text/plain") }},
		{model.SearchMediaVoiceNote, &tg.InputMessagesFilterVoice{}, voiceMessage},
	} {
		for _, pinned := range []bool{false, true} {
			for _, query := range []string{"", "caption"} {
				t.Run(string(spec.kind)+"/"+query, func(t *testing.T) {
					calls := 0
					account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
						switch q := in.(type) {
						case *tg.UpdatesGetStateRequest:
							return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
						case *tg.MessagesSearchRequest:
							calls++
							expected := spec.filter
							if pinned {
								expected = &tg.InputMessagesFilterPinned{}
							}
							if reflect.TypeOf(q.Filter) != reflect.TypeOf(expected) {
								t.Fatal("wrong provider filter", q.Filter)
							}
							if _, ok := q.Peer.(*tg.InputPeerSelf); !ok {
								t.Fatal("wrong peer")
							}
							if q.Q != query || q.OffsetID != 21 || q.MinID != 9 || q.MaxID != 21 || q.Limit != 2 || q.Hash != 0 {
								t.Fatal("search bounds lost")
							}
							m := spec.message()
							m.ID = 20
							m.Pinned = pinned
							m.Message = ""
							return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{m}})
						default:
							t.Fatalf("unexpected RPC %T", in)
							return nil
						}
					})
					rows, err := account.Search(context.Background(), model.SearchQuery{Peer: testSelfPeer(t), Query: query, MediaType: spec.kind, PinnedOnly: pinned, MinID: 10, MaxID: 20, Limit: 2})
					if err != nil || len(rows) != 1 || calls != 1 || !safeMedia(rows[0]) || rows[0].Message.Text != "" || rows[0].Message.Pinned != pinned {
						t.Fatal("media search normalization failed", err)
					}
				})
			}
		}
	}
}
