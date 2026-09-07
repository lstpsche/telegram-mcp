package model

// Folder is transient human discovery metadata, never access authority.
type Folder struct {
	ID    int32    `json:"id"`
	Title string   `json:"title"`
	Peers []PeerID `json:"peers,omitempty"`
}
