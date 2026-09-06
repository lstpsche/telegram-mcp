package telegram

import (
	"time"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

// Origin metadata is copied without resolving entities or fetching the source.
func normalizeForward(header tg.MessageFwdHeader) *model.Forward {
	_, psa := header.GetPsaType()
	if header.Imported || psa || header.PsaType != "" || header.Date <= 0 {
		return nil
	}
	forward := &model.Forward{Date: time.Unix(int64(header.Date), 0).UTC().Format(time.RFC3339), FromName: header.FromName, PostAuthor: header.PostAuthor}
	if header.FromID != nil {
		var kind model.PeerKind
		var id int64
		switch peer := header.FromID.(type) {
		case *tg.PeerUser:
			kind, id = model.PeerKindUser, peer.UserID
		case *tg.PeerChannel:
			kind, id = model.PeerKindChannel, peer.ChannelID
		case *tg.PeerChat:
			kind, id = model.PeerKindChat, peer.ChatID
		default:
			return nil
		}
		peer, err := model.NewPeerID(kind, id)
		if err != nil {
			return nil
		}
		forward.FromPeer = peer.String()
	}
	if forward.Validate() != nil {
		return nil
	}
	return forward
}
