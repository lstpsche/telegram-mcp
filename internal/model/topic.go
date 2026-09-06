package model

// Topic is untrusted, transient forum metadata. ID is usable wherever a peer is accepted.
type Topic struct {
	ID          PeerID `json:"id"`
	Title       string `json:"title"`
	Closed      bool   `json:"closed"`
	Hidden      bool   `json:"hidden"`
	UnreadCount int    `json:"unread_count"`
}

type TopicPosition struct {
	Date    int `json:"date"`
	Message int `json:"message"`
	Topic   int `json:"topic"`
}

type TopicPage struct {
	Items []Topic
	Next  *TopicPosition
}
