package model

// DialogPosition is an API navigation boundary, never access authority. Hashes
// and incidental message contents remain inside the Telegram adapter.
type DialogPosition struct {
	Folder    int    `json:"folder"`
	Peer      string `json:"peer"`
	MessageID int32  `json:"message_id"`
	Date      int32  `json:"date"`
}

func (p DialogPosition) Valid() bool {
	if p.Folder < 0 || p.Folder > 1 {
		return false
	}
	if p.Peer == "" {
		return p.MessageID == 0 && p.Date == 0
	}
	_, err := ParsePeerID(p.Peer)
	return err == nil && p.MessageID > 0 && p.Date > 0
}

type DialogEntry struct {
	Chat   Chat
	Unread Unread
}

type DialogPage struct {
	Items   []DialogEntry
	Next    *DialogPosition
	Scanned int
}
