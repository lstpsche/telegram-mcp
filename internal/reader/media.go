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

type mediaKind uint8

const (
	mediaImage mediaKind = iota
	mediaDocument
	mediaVoice
)
const mediaLifetime = 5 * time.Minute

type MediaContent struct {
	Data     []byte
	MIMEType string
}

type documentBackend interface {
	DownloadDocument(context.Context, model.Candidate) ([]byte, error)
}

type imageBackend interface {
	DownloadImage(context.Context, model.Candidate) ([]byte, error)
}

type mediaAuthority struct {
	Epoch    string `json:"epoch"`
	Revision int64  `json:"revision"`
}

type mediaHandle struct {
	Operation string          `json:"operation"`
	Message   model.MessageID `json:"message"`
	Digest    string          `json:"digest"`
	Authority mediaAuthority  `json:"authority"`
	Expires   int64           `json:"expires"`
}

func (s *Service) mediaMAC(domain, value string) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte(domain + "\x00"))
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func (s *Service) mediaDigest(source model.MediaSource, kind mediaKind) string {
	domain := "image-source-v1"
	if kind == mediaDocument {
		domain = "document-source-v1"
	} else if kind == mediaVoice {
		domain = "voice-source-v1"
	}
	encoded, _ := json.Marshal(source) // Fixed scalar fields cannot fail to encode.
	return base64.RawURLEncoding.EncodeToString(s.mediaMAC(domain, string(encoded)))
}

func (s *Service) signMediaHandle(handle mediaHandle, kind mediaKind) (string, error) {
	_, prefix, domain := mediaOperation(kind)
	encoded, err := json.Marshal(handle)
	if err != nil {
		return "", model.TextError(model.ErrorInternal, err)
	}
	payload := base64.RawURLEncoding.EncodeToString(encoded)
	return prefix + "." + payload + "." + base64.RawURLEncoding.EncodeToString(s.mediaMAC(domain, payload)), nil
}

func (s *Service) imageDescriptor(candidate model.Candidate, grant policy.Grant, authority mediaAuthority) (*model.ImageDescriptor, error) {
	if candidate.Image == nil {
		return nil, nil
	}
	if !grant.Images {
		return nil, model.TextError(model.ErrorPolicyDenied, nil)
	}
	source := *candidate.Image
	if candidate.Document != nil || candidate.Voice != nil || source.IsDocument() || source.IsVoice() {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	deadline := s.now().Add(mediaLifetime)
	deadline = grant.Deadline(deadline)
	handle := mediaHandle{Operation: "open_image", Message: candidate.Message.ID, Digest: s.mediaDigest(source, mediaImage), Authority: authority, Expires: deadline.Unix()}
	token, err := s.signMediaHandle(handle, mediaImage)
	if err != nil {
		return nil, err
	}
	return &model.ImageDescriptor{Handle: token, Kind: source.Kind, MIMEType: source.MIMEType, Width: source.Width, Height: source.Height, Size: source.Size}, nil
}

func mediaOperation(kind mediaKind) (operation, prefix, domain string) {
	if kind == mediaDocument {
		return "open_document", "doc1", "document-handle-v1"
	}
	if kind == mediaVoice {
		return "open_voice_note", "vn1", "voice-handle-v1"
	}
	return "open_image", "im1", "image-handle-v1"
}

func (s *Service) decodeMediaHandle(token string, kind mediaKind) (mediaHandle, error) {
	operation, prefix, domain := mediaOperation(kind)
	invalid := func() (mediaHandle, error) { return mediaHandle{}, model.TextError(model.ErrorInvalidReference, nil) }
	if len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != prefix {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[2] || !hmac.Equal(signature, s.mediaMAC(domain, parts[1])) {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return invalid()
	}
	handle, err := model.DecodeStrict[mediaHandle](payload)
	if err != nil {
		return invalid()
	}
	canonical, err := json.Marshal(handle)
	if err != nil || !bytes.Equal(payload, canonical) || handle.Operation != operation || handle.Message.String() == "" || len(handle.Digest) != 43 || handle.Authority.Epoch == "" || handle.Authority.Revision < 0 || handle.Expires > s.now().Add(mediaLifetime).Unix() {
		return invalid()
	}
	if handle.Expires <= s.now().Unix() {
		return mediaHandle{}, model.TextError(model.ErrorResourceExpired, nil)
	}
	return handle, nil
}

type imageItem struct {
	AlbumID     string                 `json:"album_id,omitempty"`
	ReplyTo     *model.MessageID       `json:"reply_to,omitempty"`
	ChannelPost *model.ChannelPost     `json:"channel_post,omitempty"`
	Forward     *model.Forward         `json:"forward,omitempty"`
	ID          model.MessageID        `json:"id"`
	Author      model.PeerID           `json:"author"`
	Date        string                 `json:"date"`
	Image       *model.ImageDescriptor `json:"image"`
}

// OpenImage releases original bytes only after authorization, validation, a hooked
// receipt and a final exact-source check. Remote edits are not an atomic snapshot.
func (s *Service) OpenImage(ctx context.Context, requestID, token string) (result Result, resultErr error) {
	return s.openMedia(ctx, requestID, token, mediaImage)
}

func (s *Service) OpenDocument(ctx context.Context, requestID, token string) (Result, error) {
	return s.openMedia(ctx, requestID, token, mediaDocument)
}

func mediaSource(candidate model.Candidate, kind mediaKind) *model.MediaSource {
	count := 0
	for _, source := range []*model.MediaSource{candidate.Image, candidate.Document, candidate.Voice} {
		if source != nil {
			count++
		}
	}
	if count != 1 {
		return nil
	}
	switch kind {
	case mediaImage:
		return candidate.Image
	case mediaDocument:
		return candidate.Document
	case mediaVoice:
		return candidate.Voice
	}
	return nil
}

func (s *Service) openMedia(ctx context.Context, requestID, token string, kind mediaKind) (result Result, resultErr error) {
	operation, _, _ := mediaOperation(kind)
	handle, err := s.decodeMediaHandle(token, kind)
	if err != nil {
		return Result{}, err
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	var download func(context.Context, model.Candidate) ([]byte, error)
	if kind == mediaVoice {
		backend, ok := s.backend.(voiceBackend)
		if !ok {
			return Result{}, model.TextError(model.ErrorNotReady, nil)
		}
		download = backend.DownloadVoice
	} else if kind == mediaDocument {
		backend, ok := s.backend.(documentBackend)
		if !ok {
			return Result{}, model.TextError(model.ErrorNotReady, nil)
		}
		download = backend.DownloadDocument
	} else {
		backend, ok := s.backend.(imageBackend)
		if !ok {
			return Result{}, model.TextError(model.ErrorNotReady, nil)
		}
		download = backend.DownloadImage
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
		resultErr = s.finish(ctx, lease, requestID, operation, count, attempted, resultErr, checks...)
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
	if (kind == mediaImage && !grant.Images) || (kind == mediaDocument && !grant.Documents) || (kind == mediaVoice && !grant.VoiceNotes) || (grant.Profile == policy.ProfileSelfAuthored && grant.Author != s.backend.SelfID()) || handle.Authority != (mediaAuthority{epoch, revision}) || handle.Expires > grant.Deadline(s.now().Add(mediaLifetime)).Unix() {
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
		if len(candidates) != 1 || candidates[0].Message.ID != handle.Message || mediaSource(candidates[0], kind) == nil {
			return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		candidate := candidates[0]
		if err := grant.CheckMessage(candidate, s.backend.SelfID(), s.now()); err != nil {
			return model.Candidate{}, err
		}
		media := mediaSource(candidate, kind)
		if media.IsDocument() != (kind == mediaDocument) || media.IsVoice() != (kind == mediaVoice) {
			return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if err := media.Validate(); err != nil {
			return model.Candidate{}, err
		}
		date, err := time.Parse(time.RFC3339Nano, candidate.Message.Date)
		if err != nil || date.IsZero() || !candidate.Message.ValidAuthor() || s.mediaDigest(*media, kind) != handle.Digest {
			return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		return candidate, check()
	}
	candidate, err := source()
	if err != nil {
		return Result{}, err
	}
	if candidate.Message.Forward != nil {
		attribution := *candidate.Message.Forward
		candidate.Message.Forward = &attribution
	}
	if candidate.Message.ThreadRoot != nil {
		root := *candidate.Message.ThreadRoot
		candidate.Message.ThreadRoot = &root
	}
	if candidate.Message.ReplyTo != nil {
		parent := *candidate.Message.ReplyTo
		candidate.Message.ReplyTo = &parent
	}
	if candidate.Message.ChannelPost != nil {
		post := *candidate.Message.ChannelPost
		candidate.Message.ChannelPost = &post
	}
	immutableSource := *mediaSource(candidate, kind)
	if kind == mediaVoice {
		candidate.Voice = &immutableSource
	} else if kind == mediaDocument {
		candidate.Document = &immutableSource
	} else {
		candidate.Image = &immutableSource
	}
	data, err := download(ctx, candidate)
	downloaded = data
	if err != nil {
		return Result{}, err
	}
	if err := check(); err != nil {
		return Result{}, err
	}
	if kind == mediaVoice {
		err = validateVoiceData(immutableSource, data)
	} else if kind == mediaDocument {
		err = validateDocumentData(immutableSource, data)
	} else {
		err = validateImageData(immutableSource, data)
	}
	if err != nil {
		return Result{}, err
	}
	current, err := source()
	if err != nil {
		return Result{}, err
	}
	if !sameMediaAttribution(current.Message, candidate.Message) {
		return Result{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	effect := model.ReadEffect{Kind: model.ReadEffectHistoryMarkedRead, ThroughMessageID: &handle.Message}
	if kind == mediaVoice {
		descriptor := &model.VoiceDescriptor{Handle: token, MIMEType: immutableSource.MIMEType, Size: immutableSource.Size, Duration: immutableSource.Duration}
		result, err = prepare(requestID, []voiceItem{{AlbumID: candidate.Message.AlbumID, ReplyTo: candidate.Message.ReplyTo, ChannelPost: candidate.Message.ChannelPost, Forward: candidate.Message.Forward, ID: handle.Message, Author: candidate.Message.Author, Date: candidate.Message.Date, Voice: descriptor}}, s.now(), false, effect, nil, nil)
	} else if kind == mediaDocument {
		descriptor := &model.DocumentDescriptor{Handle: token, MIMEType: immutableSource.MIMEType, Size: immutableSource.Size}
		result, err = prepare(requestID, []documentItem{{AlbumID: candidate.Message.AlbumID, ReplyTo: candidate.Message.ReplyTo, ChannelPost: candidate.Message.ChannelPost, Forward: candidate.Message.Forward, ID: handle.Message, Author: candidate.Message.Author, Date: candidate.Message.Date, Document: descriptor}}, s.now(), false, effect, nil, nil)
	} else {
		descriptor := &model.ImageDescriptor{Handle: token, Kind: immutableSource.Kind, MIMEType: immutableSource.MIMEType, Width: immutableSource.Width, Height: immutableSource.Height, Size: immutableSource.Size}
		result, err = prepare(requestID, []imageItem{{AlbumID: candidate.Message.AlbumID, ReplyTo: candidate.Message.ReplyTo, ChannelPost: candidate.Message.ChannelPost, Forward: candidate.Message.Forward, ID: handle.Message, Author: candidate.Message.Author, Date: candidate.Message.Date, Image: descriptor}}, s.now(), false, effect, nil, nil)
	}
	if err != nil {
		return Result{}, err
	}
	// Text serialization already checks both metadata mirrors. Add the single
	// native content block and bounded resource URI plus JSON-RPC framing before any read effect.
	// PDFs retain their original size; image, voice and plain-text budgets remain.
	if kind != mediaDocument || immutableSource.MIMEType != "application/pdf" {
		contentBytes := base64.StdEncoding.EncodedLen(len(data))
		if kind == mediaDocument && immutableSource.MIMEType == "text/plain" {
			encoded, encodeErr := json.Marshal(string(data))
			if encodeErr != nil {
				return Result{}, model.TextError(model.ErrorInternal, encodeErr)
			}
			contentBytes = len(encoded)
		}
		if contentBytes+model.MaximumTextResultBytes+8192 > model.MaximumMediaResultBytes {
			return Result{}, model.TextError(model.ErrorResultTooLarge, nil)
		}
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
	if !sameMediaAttribution(current.Message, candidate.Message) {
		return Result{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	if kind == mediaVoice {
		result.Voice = &MediaContent{Data: data, MIMEType: immutableSource.MIMEType}
	} else if kind == mediaDocument {
		result.Document = &DocumentContent{Data: data, MIMEType: immutableSource.MIMEType, URI: "telegram-document:" + token}
	} else {
		result.Image = &MediaContent{Data: data, MIMEType: immutableSource.MIMEType}
	}
	count = 1
	return result, nil
}

func sameChannelPost(a, b *model.ChannelPost) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameReply(a, b *model.MessageID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameMediaAttribution(a, b model.Message) bool {
	return a.AlbumID == b.AlbumID && a.Author == b.Author && a.Date == b.Date &&
		model.SameForward(a.Forward, b.Forward) && sameChannelPost(a.ChannelPost, b.ChannelPost) && sameReply(a.ReplyTo, b.ReplyTo) && sameReply(a.ThreadRoot, b.ThreadRoot) && a.DiscussionPeer == b.DiscussionPeer
}
