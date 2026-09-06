package model

import (
	"math"
	"strings"
	"testing"
)

func TestAlbumIdentityIsExactPeerScopedAndLossless(t *testing.T) {
	peer, _ := NewPeerID(PeerKindChannel, 42)
	topic, _ := NewTopicPeer(peer, 7)
	seen := map[string]bool{}
	for _, p := range []PeerID{peer, topic} {
		for _, group := range []int64{1, math.MaxInt64, math.MinInt64, -1} {
			album, err := NewAlbumID(p, group)
			id, _ := NewMessageID(p, 20)
			m := Message{ID: id, AlbumID: album}
			if err != nil || !m.ValidAlbum() || seen[album] {
				t.Fatal("invalid or colliding album", err)
			}
			seen[album] = true
			other, _ := NewMessageID(PeerID{}, 20)
			m.ID = other
			if m.ValidAlbum() {
				t.Fatal("unscoped album accepted")
			}
		}
	}
	id, _ := NewMessageID(peer, 20)
	for _, album := range []string{"tgalbum:v1:channel:42:0000000000000000", "tgalbum:v1:channel:42:fffffffffffffffF", "tgalbum:v1:channel:42:1", "tgalbum:v1:channel:43:0000000000000001", "tgalbum:v1:channel:42:topic:7:0000000000000001"} {
		if (Message{ID: id, AlbumID: album}).ValidAlbum() {
			t.Fatal("invalid album accepted")
		}
	}
	if _, err := NewAlbumID(peer, 0); err == nil {
		t.Fatal("zero group accepted")
	}
	album, _ := NewAlbumID(peer, -1)
	if !strings.HasSuffix(album, "ffffffffffffffff") {
		t.Fatal("signed bits lost")
	}
}
