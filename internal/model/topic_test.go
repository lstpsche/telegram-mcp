package model

import "testing"

func TestTopicReferencesPreserveExactAuthority(t *testing.T) {
	parent, _ := ParsePeerID("tgpeer:v1:channel:42")
	for _, id := range []int32{1, 7, 2147483647} {
		peer, err := NewTopicPeer(parent, id)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParsePeerID(peer.String())
		if err != nil || parsed != peer || peer.Parent() != parent || peer == parent || peer.TopicID() != id {
			t.Fatal(peer, err)
		}
		message, err := NewMessageID(peer, 20)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := ParseMessageID(message.String())
		if err != nil || decoded != message || decoded.Peer() != peer {
			t.Fatal(message, err)
		}
		if _, err := NewTopicPeer(peer, 8); err == nil {
			t.Fatal("nested topic accepted")
		}
	}
	for _, raw := range []string{"tgpeer:v1:channel:42:topic:0", "tgpeer:v1:channel:42:topic:01", "tgpeer:v1:channel:42:topic:2147483648", "tgpeer:v1:user:42:topic:7", "tgpeer:v1:chat:42:topic:7", "tgpeer:v1:channel:42:thread:7"} {
		if _, err := ParsePeerID(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
