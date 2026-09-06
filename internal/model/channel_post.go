package model

import "unicode/utf8"

// ChannelPost contains display attribution; the containing channel is the publisher.
type ChannelPost struct {
	Sender    string `json:"sender,omitempty"`
	Signature string `json:"signature,omitempty"`
}

func (m Message) ValidAuthor() bool {
	if m.ChannelPost == nil {
		return m.Author.Kind() == PeerKindUser && m.Author.String() != ""
	}
	if m.Author.Kind() != PeerKindChannel || m.Author.TopicID() != 0 || m.Author != m.ID.Peer() {
		return false
	}
	post := m.ChannelPost
	if !utf8.ValidString(post.Signature) || len(post.Signature) > 4096 {
		return false
	}
	if post.Sender != "" {
		peer, err := ParsePeerID(post.Sender)
		if err != nil || (peer.Kind() != PeerKindUser && peer.Kind() != PeerKindChannel) || peer.TopicID() != 0 {
			return false
		}
	}
	return true
}
