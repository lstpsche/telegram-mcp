package policy

import (
	"errors"
	"math"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

const (
	ProfileSelfAuthored  = "self-authored"
	ProfileConsented     = "consented"
	MaximumGrants        = 20
	MaximumGrantLifetime = 30 * 24 * time.Hour
)

var (
	ErrBusy         = errors.New("policy is in use")
	ErrDenied       = errors.New("no applicable text grant")
	ErrInvalidGrant = errors.New("invalid text grant")
	ErrEpochChanged = errors.New("authorization epoch is unavailable or changed")
	ErrClosed       = errors.New("policy lease is closed")
)

// Grant is resolved under a policy lease from an exact human grant or explicit
// Full read access. Eligible belongs only to persisted restricted grants.
type Grant struct {
	Peer        model.PeerID
	Author      model.PeerID
	MinID       int32
	MaxID       int32
	ReadThrough int32
	Profile     string
	ExpiresAt   time.Time
	Eligible    bool
	Images      bool
	Documents   bool
	VoiceNotes  bool
	fullRead    bool
}

// Channel publisher grants cover posts only and require a consented basis.
func (g Grant) ValidAuthor() bool {
	return g.Author.Kind() == model.PeerKindUser || (g.Profile == ProfileConsented && g.Author.Kind() == model.PeerKindChannel && g.Author.TopicID() == 0 && g.Author == g.Peer)
}

func (g Grant) validateFields() error {
	if g.Peer.String() == "" || !g.ValidAuthor() || g.Author.String() == "" ||
		g.MinID <= 0 || g.MaxID < g.MinID || g.ReadThrough < 0 ||
		(g.Profile != ProfileSelfAuthored && g.Profile != ProfileConsented) ||
		!g.Eligible || g.ExpiresAt.IsZero() {
		return model.TextError(model.ErrorInvalidInput, ErrInvalidGrant)
	}
	return nil
}

func (g Grant) Validate(now time.Time) error {
	if err := g.validateFields(); err != nil {
		return err
	}
	if now.IsZero() || !g.ExpiresAt.After(now) || g.ExpiresAt.After(now.Add(MaximumGrantLifetime)) {
		return model.TextError(model.ErrorInvalidInput, ErrInvalidGrant)
	}
	return nil
}

// Deadline caps transient work without inventing an expiry for account authority.
func (g Grant) Deadline(limit time.Time) time.Time {
	if !g.fullRead && g.ExpiresAt.Before(limit) {
		return g.ExpiresAt
	}
	return limit
}

func fullReadGrant(peer model.PeerID) Grant {
	return Grant{Peer: peer, MinID: 1, MaxID: math.MaxInt32, ReadThrough: math.MaxInt32, Images: true, Documents: true, VoiceNotes: true, fullRead: true}
}

func (g Grant) CheckCurrent(now time.Time) error {
	if g.fullRead {
		if g.Peer.String() == "" || now.IsZero() {
			return model.TextError(model.ErrorPolicyDenied, ErrDenied)
		}
		return nil
	}

	if err := g.Validate(now); err != nil {
		return model.TextError(model.ErrorPolicyDenied, ErrDenied)
	}
	return nil
}

func (g Grant) CheckMessage(candidate model.Candidate, self model.PeerID, now time.Time) error {
	if err := g.CheckCurrent(now); err != nil {
		return err
	}
	message := candidate.Message
	if !message.ValidAlbum() || (message.AlbumID != "" && candidate.Image == nil && candidate.Document == nil && candidate.Voice == nil) {
		return model.TextError(model.ErrorPolicyDenied, ErrDenied)
	}
	if !message.Reactions.Valid(message.ID.Peer()) || !message.LinkPreview.Valid() || !message.Poll.Valid() {
		return model.TextError(model.ErrorPolicyDenied, ErrDenied)
	}
	if !message.ValidReply() || !message.ValidAuthor() || message.ID.Peer() != g.Peer || message.ID.TelegramID() < g.MinID ||
		message.ID.TelegramID() > g.MaxID || (!g.fullRead && message.Author != g.Author) ||
		(g.Profile == ProfileSelfAuthored && (self.Kind() != model.PeerKindUser || message.Author != self)) {
		return model.TextError(model.ErrorPolicyDenied, ErrDenied)
	}
	if candidate.Protected {
		return model.TextError(model.ErrorProtectedContent, ErrDenied)
	}
	if candidate.Ephemeral {
		return model.TextError(model.ErrorEphemeralContent, ErrDenied)
	}
	if candidate.Forwarded != (message.Forward != nil) || (message.Forward != nil && (message.Forward.Validate() != nil || g.Profile == ProfileSelfAuthored)) {
		return model.TextError(model.ErrorPolicyDenied, ErrDenied)
	}
	if candidate.Quoted || candidate.Unsupported || (candidate.Image != nil && !g.Images) || (candidate.Document != nil && !g.Documents) || (candidate.Voice != nil && !g.VoiceNotes) {
		return model.TextError(model.ErrorPolicyDenied, ErrDenied)
	}
	return nil
}

func (g Grant) CheckRead(through int32, now time.Time) error {
	if err := g.CheckCurrent(now); err != nil {
		return err
	}
	if through <= 0 || through > g.ReadThrough {
		return model.TextError(model.ErrorPolicyDenied, ErrDenied)
	}
	return nil
}
