package reader

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

const imageLifetime = 5 * time.Minute

type ImageContent struct {
	Data     []byte
	MIMEType string
}

type imageBackend interface {
	DownloadImage(context.Context, model.Candidate) ([]byte, error)
}

type imageAuthority struct {
	Epoch    string `json:"epoch"`
	Revision int64  `json:"revision"`
}

type imageHandle struct {
	Operation string          `json:"operation"`
	Message   model.MessageID `json:"message"`
	Digest    string          `json:"digest"`
	Authority imageAuthority  `json:"authority"`
	Expires   int64           `json:"expires"`
}

func (s *Service) imageMAC(domain, value string) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte(domain + "\x00"))
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func (s *Service) imageDigest(source model.ImageSource) string {
	// ImageSource contains only scalar fields, so marshaling cannot fail.
	encoded, _ := json.Marshal(source)
	return base64.RawURLEncoding.EncodeToString(s.imageMAC("image-source-v1", string(encoded)))
}

func (s *Service) imageDescriptor(candidate model.Candidate, grant policy.Grant, authority imageAuthority) (*model.ImageDescriptor, error) {
	if candidate.Image == nil {
		return nil, nil
	}
	if !grant.Images {
		return nil, model.TextError(model.ErrorPolicyDenied, nil)
	}
	source := *candidate.Image
	if err := source.Validate(); err != nil {
		return nil, err
	}
	deadline := s.now().Add(imageLifetime)
	if grant.ExpiresAt.Before(deadline) {
		deadline = grant.ExpiresAt
	}
	handle := imageHandle{Operation: "open_image", Message: candidate.Message.ID, Digest: s.imageDigest(source), Authority: authority, Expires: deadline.Unix()}
	encoded, err := json.Marshal(handle)
	if err != nil {
		return nil, model.TextError(model.ErrorInternal, err)
	}
	payload := base64.RawURLEncoding.EncodeToString(encoded)
	token := "im1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.imageMAC("image-handle-v1", payload))
	return &model.ImageDescriptor{Handle: token, Kind: source.Kind, MIMEType: source.MIMEType, Width: source.Width, Height: source.Height, Size: source.Size}, nil
}

func (s *Service) decodeImageHandle(token string) (imageHandle, error) {
	invalid := func() (imageHandle, error) { return imageHandle{}, model.TextError(model.ErrorInvalidReference, nil) }
	if len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "im1" {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[2] || !hmac.Equal(signature, s.imageMAC("image-handle-v1", parts[1])) {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return invalid()
	}
	handle, err := model.DecodeStrict[imageHandle](payload)
	if err != nil {
		return invalid()
	}
	canonical, err := json.Marshal(handle)
	if err != nil || !bytes.Equal(payload, canonical) || handle.Operation != "open_image" || handle.Message.String() == "" || len(handle.Digest) != 43 || handle.Authority.Epoch == "" || handle.Authority.Revision < 0 || handle.Expires > s.now().Add(imageLifetime).Unix() {
		return invalid()
	}
	if handle.Expires <= s.now().Unix() {
		return imageHandle{}, model.TextError(model.ErrorResourceExpired, nil)
	}
	return handle, nil
}

type imageItem struct {
	ID     model.MessageID        `json:"id"`
	Author model.PeerID           `json:"author"`
	Date   string                 `json:"date"`
	Image  *model.ImageDescriptor `json:"image"`
}

// OpenImage releases original bytes only after authorization, validation, a hooked
// receipt and a final exact-source check. Remote edits are not an atomic snapshot.
func (s *Service) OpenImage(ctx context.Context, requestID, token string) (result Result, resultErr error) {
	handle, err := s.decodeImageHandle(token)
	if err != nil {
		return Result{}, err
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	backend, ok := s.backend.(imageBackend)
	if !ok {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	ctx, stopExpiry := context.WithTimeout(ctx, time.Unix(handle.Expires, 0).Sub(s.now()))
	defer stopExpiry()
	lease, err := s.policy.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	attempted, count := false, 0
	var beforeRelease func() error
	var downloaded []byte
	defer func() {
		checks := []func() error{}
		if beforeRelease != nil {
			checks = append(checks, beforeRelease)
		}
		resultErr = s.finish(ctx, lease, requestID, "open_image", count, attempted, resultErr, checks...)
		if resultErr != nil {
			clear(downloaded)
			result = Result{}
		}
	}()
	grant, err := lease.Grant(ctx, handle.Message.Peer())
	if err != nil {
		return Result{}, err
	}
	epoch, revision, err := lease.Binding(ctx)
	if err != nil {
		return Result{}, err
	}
	if !grant.Images || (grant.Profile == policy.ProfileSelfAuthored && grant.Author != s.backend.SelfID()) || handle.Authority != (imageAuthority{epoch, revision}) || handle.Expires > grant.ExpiresAt.Unix() {
		return Result{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !s.Ready() {
			return model.TextError(model.ErrorFreshnessDegraded, nil)
		}
		if handle.Expires <= s.now().Unix() {
			return model.TextError(model.ErrorResourceExpired, nil)
		}
		if grant.Profile == policy.ProfileSelfAuthored && grant.Author != s.backend.SelfID() {
			return model.TextError(model.ErrorPolicyDenied, nil)
		}
		if handle.Message.TelegramID() < grant.MinID || handle.Message.TelegramID() > grant.MaxID {
			return model.TextError(model.ErrorPolicyDenied, nil)
		}
		return grant.CheckRead(handle.Message.TelegramID(), s.now())
	}
	beforeRelease = check
	source := func() (model.Candidate, error) {
		if err := check(); err != nil {
			return model.Candidate{}, err
		}
		candidates, err := s.backend.History(ctx, model.HistoryQuery{Peer: grant.Peer, Target: handle.Message.TelegramID(), MinID: grant.MinID, MaxID: grant.MaxID, Limit: 1})
		if err != nil {
			return model.Candidate{}, err
		}
		if len(candidates) != 1 || candidates[0].Message.ID != handle.Message || candidates[0].Image == nil {
			return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		candidate := candidates[0]
		if err := grant.CheckMessage(candidate, s.backend.SelfID(), s.now()); err != nil {
			return model.Candidate{}, err
		}
		if err := candidate.Image.Validate(); err != nil {
			return model.Candidate{}, err
		}
		date, err := time.Parse(time.RFC3339Nano, candidate.Message.Date)
		if err != nil || date.IsZero() || candidate.Message.Author.Kind() != model.PeerKindUser || s.imageDigest(*candidate.Image) != handle.Digest {
			return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		return candidate, check()
	}
	candidate, err := source()
	if err != nil {
		return Result{}, err
	}
	imageSource := *candidate.Image
	candidate.Image = &imageSource
	data, err := backend.DownloadImage(ctx, candidate)
	if err != nil {
		return Result{}, err
	}
	downloaded = data
	if err := check(); err != nil {
		return Result{}, err
	}
	if err := validateImageData(*candidate.Image, data); err != nil {
		return Result{}, err
	}
	current, err := source()
	if err != nil {
		return Result{}, err
	}
	if current.Message.Author != candidate.Message.Author || current.Message.Date != candidate.Message.Date {
		return Result{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	descriptor := &model.ImageDescriptor{Handle: token, Kind: candidate.Image.Kind, MIMEType: candidate.Image.MIMEType, Width: candidate.Image.Width, Height: candidate.Image.Height, Size: candidate.Image.Size}
	result, err = prepare(requestID, []imageItem{{ID: handle.Message, Author: candidate.Message.Author, Date: candidate.Message.Date, Image: descriptor}}, s.now(), false, model.ReadEffect{Kind: model.ReadEffectHistoryMarkedRead, ThroughMessageID: &handle.Message}, nil, nil)
	if err != nil {
		return Result{}, err
	}
	// Text serialization already checks both metadata mirrors. Add the single
	// native base64 block and ample JSON-RPC framing before any read effect.
	if base64.StdEncoding.EncodedLen(len(data))+model.MaximumTextResultBytes+1024 > model.MaximumImageResultBytes {
		return Result{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	if err := check(); err != nil {
		return Result{}, err
	}
	attempted = true
	if err := s.backend.Acknowledge(ctx, grant.Peer, handle.Message.TelegramID()); err != nil {
		return Result{}, err
	}
	current, err = source()
	if err != nil {
		return Result{}, err
	}
	if current.Message.Author != candidate.Message.Author || current.Message.Date != candidate.Message.Date {
		return Result{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	result.Image = &ImageContent{Data: data, MIMEType: candidate.Image.MIMEType}
	count = 1
	return result, nil
}
