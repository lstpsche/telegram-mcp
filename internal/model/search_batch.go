package model

const MaximumSearchRequests = 10

type SearchRequest struct {
	Peer   PeerID
	Scope  ScopeID
	Filter SearchFilter
	Limit  int
	Cursor string
}

type SearchAssociation struct {
	Messages   []MessageID    `json:"messages"`
	Partial    bool           `json:"partial"`
	NextCursor *string        `json:"next_cursor"`
	Scope      *ScopeCoverage `json:"scope,omitempty"`
}
