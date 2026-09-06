package model

import (
	"fmt"
	"strconv"
	"strings"
)

// NewAlbumID describes grouping within one exact conversation, never authority.
// Hex preserves all Telegram long bits without JSON number precision loss.
func NewAlbumID(peer PeerID, groupedID int64) (string, error) {
	if peer.String() == "" || groupedID == 0 {
		return "", ErrInvalidReference
	}
	return "tgalbum:v1:" + strings.TrimPrefix(peer.String(), "tgpeer:v1:") + fmt.Sprintf(":%016x", uint64(groupedID)), nil
}

func (m Message) ValidAlbum() bool {
	if m.AlbumID == "" {
		return true
	}
	prefix := "tgalbum:v1:" + strings.TrimPrefix(m.ID.Peer().String(), "tgpeer:v1:") + ":"
	if m.ID.Peer().String() == "" || !strings.HasPrefix(m.AlbumID, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(m.AlbumID, prefix)
	value, err := strconv.ParseUint(suffix, 16, 64)
	return err == nil && value != 0 && len(suffix) == 16 && fmt.Sprintf("%016x", value) == suffix
}
