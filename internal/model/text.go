package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// MaximumRPCIDBytes bounds the normalized JSON encoding of a reflected ID.
const MaximumRPCIDBytes = 1024

// Chat and Message contain untrusted display data. They are never audit records.
type Chat struct {
	Broadcast bool   `json:"broadcast,omitempty"`
	Forum     bool   `json:"forum,omitempty"`
	ID        PeerID `json:"id"`
	Title     string `json:"title"`
}

type ReplyChain struct {
	Depth int    `json:"depth"`
	State string `json:"state"`
}

type AlbumContext struct {
	State    string      `json:"state"`
	Messages []MessageID `json:"messages"`
}

type Message struct {
	AlbumContext   *AlbumContext       `json:"album_context,omitempty"`
	URL            string              `json:"url,omitempty"`
	DiscussionRoot *MessageID          `json:"discussion_root,omitempty"`
	ThreadRoot     *MessageID          `json:"thread_root,omitempty"`
	DiscussionPeer string              `json:"discussion_peer,omitempty"`
	SavedPeer      string              `json:"saved_peer,omitempty"`
	Pinned         bool                `json:"pinned,omitempty"`
	Reactions      *Reactions          `json:"reactions,omitempty"`
	LinkPreview    *LinkPreview        `json:"link_preview,omitempty"`
	Poll           *Poll               `json:"poll,omitempty"`
	AlbumID        string              `json:"album_id,omitempty"`
	ReplyTo        *MessageID          `json:"reply_to,omitempty"`
	ReplyChain     *ReplyChain         `json:"reply_chain,omitempty"`
	ChannelPost    *ChannelPost        `json:"channel_post,omitempty"`
	Forward        *Forward            `json:"forward,omitempty"`
	Voice          *VoiceDescriptor    `json:"voice_note,omitempty"`
	Document       *DocumentDescriptor `json:"document,omitempty"`
	Image          *ImageDescriptor    `json:"image,omitempty"`
	ID             MessageID           `json:"id"`
	Author         PeerID              `json:"author"`
	Date           string              `json:"date"`
	Text           string              `json:"text"`
}

// Candidate carries normalization evidence only inside the application.
// Unsafe source bodies must not be copied into Message.Text.
type Candidate struct {
	UnreadMention bool
	Voice         *MediaSource
	// SentAt retains only timestamp evidence for date traversal, including excluded bodies.
	SentAt      int64
	Image       *MediaSource
	Document    *MediaSource
	Message     Message
	Protected   bool
	Ephemeral   bool
	Forwarded   bool
	Quoted      bool
	Unsupported bool
}

// HistoryQuery uses inclusive grant bounds and exclusive Before selection.
// Target selects context; BeforeCount/AfterCount count neighboring messages.
type HistoryQuery struct {
	ExpandAlbum       bool
	LinkUsername      string
	LinkTopic         int32
	ResolveDiscussion bool
	Peer              PeerID
	Before            int32
	ReplyDepth        int
	Target            int32
	BeforeCount       int
	AfterCount        int
	MinID             int32
	MaxID             int32
	Limit             int
}

// OperationError preserves an internal cause while its text is content-free.
// The cause is for errors.Is/As only and must never be logged or serialized.
type OperationError struct {
	Category ErrorCategory
	Cause    error
}

func (e *OperationError) Error() string { return fmt.Sprintf("text operation failed: %s", e.Category) }
func (e *OperationError) Unwrap() error { return e.Cause }

func TextError(category ErrorCategory, cause error) error {
	return &OperationError{Category: category, Cause: cause}
}

func TextErrorCategory(err error) ErrorCategory {
	if err == nil {
		return ""
	}
	var operation *OperationError
	if errors.As(err, &operation) && operation.Category.IsValid() {
		return operation.Category
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorCancelled
	}
	return ErrorInternal
}

// SearchQuery is adapter input. Query text is transient and must never be logged.
type SearchQuery struct {
	FilenameQuery        string
	UnreadMentionsOnly   bool
	ReplyTo, ThreadRoot  string
	SavedPeer            string
	SavedTag             SavedTag
	Sender               string
	Since, Until         int64
	MediaType            SearchMediaType
	PinnedOnly           bool
	Window               *DateWindow
	Peer                 PeerID
	Query                string
	MinID, MaxID, Before int32
	Limit                int
}

type SearchHit struct {
	URL              string              `json:"url,omitempty"`
	ThreadRoot       *MessageID          `json:"thread_root,omitempty"`
	DiscussionPeer   string              `json:"discussion_peer,omitempty"`
	SavedPeer        string              `json:"saved_peer,omitempty"`
	Pinned           bool                `json:"pinned,omitempty"`
	Reactions        *Reactions          `json:"reactions,omitempty"`
	HasLinkPreview   bool                `json:"has_link_preview,omitempty"`
	HasPoll          bool                `json:"has_poll,omitempty"`
	AlbumID          string              `json:"album_id,omitempty"`
	ReplyTo          *MessageID          `json:"reply_to,omitempty"`
	ChannelPost      *ChannelPost        `json:"channel_post,omitempty"`
	Forward          *Forward            `json:"forward,omitempty"`
	Voice            *VoiceDescriptor    `json:"voice_note,omitempty"`
	Document         *DocumentDescriptor `json:"document,omitempty"`
	Image            *ImageDescriptor    `json:"image,omitempty"`
	ID               MessageID           `json:"id"`
	Author           PeerID              `json:"author"`
	Date             string              `json:"date"`
	Snippet          string              `json:"snippet"`
	SnippetTruncated bool                `json:"snippet_truncated"`
}

// Unread describes the entire granted dialog, not just its authorized body range.
type Unread struct {
	Peer   PeerID `json:"peer"`
	Count  int    `json:"unread_count"`
	Marked bool   `json:"unread_mark"`
}

// SearchFilter narrows message discovery without granting content access.
type SearchFilter struct {
	FilenameQuery       string
	UnreadMentionsOnly  bool
	ReplyTo, ThreadRoot string
	SavedPeer           string
	SavedTag            SavedTag
	Sender              string
	Since, Until        int64
	MediaType           SearchMediaType
	Query               string
	PinnedOnly          bool
}

type SearchMediaType string

const (
	SearchMediaPhoto     SearchMediaType = "photo"
	SearchMediaImageFile SearchMediaType = "image_file"
	SearchMediaPDF       SearchMediaType = "pdf"
	SearchMediaTextFile  SearchMediaType = "text_file"
	SearchMediaVoiceNote SearchMediaType = "voice_note"
)

func (f SearchFilter) Normalize() (SearchFilter, error) {
	if f.FilenameQuery != "" {
		if !ValidAttachmentFilename(f.FilenameQuery) {
			return SearchFilter{}, TextError(ErrorInvalidInput, nil)
		}
		f.FilenameQuery = strings.ToLower(strings.TrimSpace(f.FilenameQuery))
		if f.FilenameQuery == "" {
			return SearchFilter{}, TextError(ErrorInvalidInput, nil)
		}
	}

	for _, ref := range []string{f.ReplyTo, f.ThreadRoot} {
		if ref != "" {
			if _, err := ParseMessageID(ref); err != nil {
				return SearchFilter{}, TextError(ErrorInvalidInput, nil)
			}
		}
	}
	if f.SavedPeer != "" {
		peer, err := ParsePeerID(f.SavedPeer)
		if err != nil || peer.TopicID() != 0 {
			return SearchFilter{}, TextError(ErrorInvalidInput, nil)
		}
	}
	if f.SavedTag != (SavedTag{}) && !f.SavedTag.Valid() {
		return SearchFilter{}, TextError(ErrorInvalidInput, nil)
	}
	if f.Sender != "" {
		peer, err := ParsePeerID(f.Sender)
		if err != nil || (peer.Kind() != PeerKindUser && peer.Kind() != PeerKindChannel) || peer.TopicID() != 0 {
			return SearchFilter{}, TextError(ErrorInvalidInput, nil)
		}
	}
	if f.Since < 0 || f.Until < 0 || f.Since > math.MaxInt32 || f.Until > math.MaxInt32 || (f.Since != 0 && f.Until != 0 && f.Until <= f.Since) {
		return SearchFilter{}, TextError(ErrorInvalidInput, nil)
	}
	switch f.MediaType {
	case "", SearchMediaPhoto, SearchMediaImageFile, SearchMediaPDF, SearchMediaTextFile, SearchMediaVoiceNote:
	default:
		return SearchFilter{}, TextError(ErrorInvalidInput, nil)
	}
	if !utf8.ValidString(f.Query) || len(f.Query) > 1024 {
		return SearchFilter{}, TextError(ErrorInvalidInput, nil)
	}
	f.Query = strings.TrimSpace(f.Query)
	if (f.Query == "" && f.FilenameQuery == "" && !f.PinnedOnly && !f.UnreadMentionsOnly && f.MediaType == "" && f.Sender == "" && f.Since == 0 && f.Until == 0 && !f.HasSavedFilter() && f.ReplyTo == "" && f.ThreadRoot == "") || utf8.RuneCountInString(f.Query) > 256 {
		return SearchFilter{}, TextError(ErrorInvalidInput, nil)
	}
	return f, nil
}

// ValidReply keeps reply navigation inside the exact conversation and older IDs.
func (m Message) ValidReply() bool {
	if m.DiscussionRoot != nil && (m.DiscussionRoot.String() == "" || m.DiscussionRoot.Peer().String() != m.DiscussionPeer) {
		return false
	}
	if m.DiscussionPeer != "" {
		peer, err := ParsePeerID(m.DiscussionPeer)
		if err != nil || peer.Kind() != PeerKindChannel || peer.TopicID() != 0 || peer == m.ID.Peer() || m.ChannelPost == nil {
			return false
		}
	}
	for _, ref := range []*MessageID{m.ReplyTo, m.ThreadRoot} {
		if ref != nil && (ref.String() == "" || ref.Peer() != m.ID.Peer() || ref.TelegramID() >= m.ID.TelegramID()) {
			return false
		}
	}
	return m.ThreadRoot == nil || (m.ReplyTo != nil && m.ThreadRoot.TelegramID() <= m.ReplyTo.TelegramID())
}

// UsesHistory selects the primary traversal when no Telegram search selector exists.
func (q SearchQuery) UsesHistory() bool {
	return q.Window != nil || (q.Query == "" && !q.PinnedOnly && !q.UnreadMentionsOnly && q.MediaType == "")
}

// CheckReplyPeer keeps selectors within one exact conversation or topic.
func (f SearchFilter) CheckReplyPeer(peer PeerID) error {
	for _, ref := range []string{f.ReplyTo, f.ThreadRoot} {
		if ref != "" {
			id, err := ParseMessageID(ref)
			if err != nil || id.Peer() != peer {
				return TextError(ErrorInvalidInput, nil)
			}
		}
	}
	return nil
}

// MaximumContextTargets bounds batch context preparation and read receipts.
const MaximumContextTargets = 20

// MessageContext associates a requested target with its deduplicated result IDs.
type MessageContext struct {
	Target   MessageID   `json:"target"`
	Messages []MessageID `json:"messages"`
	Partial  bool        `json:"partial"`
}
