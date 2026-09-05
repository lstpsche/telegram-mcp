package policy

import (
	"errors"
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

// Grant authorizes exact content and, separately, the dialog prefix affected by
// read acknowledgments. Eligible is an affirmative human attestation.
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
}

func (g Grant) validateFields() error {
	if g.Peer.String() == "" || g.Author.Kind() != model.PeerKindUser || g.Author.String() == "" ||
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

func (g Grant) CheckCurrent(now time.Time) error {
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
	if message.ID.Peer() != g.Peer || message.ID.TelegramID() < g.MinID ||
		message.ID.TelegramID() > g.MaxID || message.Author != g.Author ||
		(g.Profile == ProfileSelfAuthored && (self.Kind() != model.PeerKindUser || message.Author != self)) {
		return model.TextError(model.ErrorPolicyDenied, ErrDenied)
	}
	if candidate.Protected {
		return model.TextError(model.ErrorProtectedContent, ErrDenied)
	}
	if candidate.Ephemeral {
		return model.TextError(model.ErrorEphemeralContent, ErrDenied)
	}
	if candidate.Forwarded || candidate.Quoted || candidate.Unsupported || (candidate.Image != nil && !g.Images) {
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
