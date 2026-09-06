package reader

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type topicBackend interface {
	Topics(context.Context, model.PeerID, model.TopicPosition, int) (model.TopicPage, error)
	Topic(context.Context, model.PeerID) (model.Topic, error)
}

type topicCursor struct {
	Peer      model.PeerID        `json:"peer"`
	Position  model.TopicPosition `json:"position"`
	Limit     int                 `json:"limit"`
	Authority mediaAuthority      `json:"authority"`
	Expires   int64               `json:"expires"`
}

func (s *Service) ListTopics(ctx context.Context, requestID string, peer model.PeerID, limit int, token string) (result Result, resultErr error) {
	if peer.Kind() != model.PeerKindChannel || peer.TopicID() != 0 || model.ValidatePageSize(limit) != nil {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	backend, ok := s.backend.(topicBackend)
	if !ok {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	lease, err := s.policy.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	count := 0
	grants := []policy.Grant{}
	expires := s.now().Add(5 * time.Minute).Unix()
	check := func() error {
		if err := s.checkGrantsCurrent(ctx, grants); err != nil {
			return err
		}
		if s.now().Unix() >= expires {
			return model.TextError(model.ErrorCursorExpired, nil)
		}
		return nil
	}
	defer func() {
		resultErr = s.finish(ctx, lease, requestID, "list_topics", count, false, resultErr, check)
		if resultErr != nil {
			result = Result{}
		}
	}()
	full, err := lease.FullRead(ctx)
	if err != nil {
		return Result{}, err
	}
	items := []model.Topic{}
	var next *string
	if full {
		epoch, revision, err := lease.Binding(ctx)
		if err != nil {
			return Result{}, err
		}
		cursor := topicCursor{Peer: peer, Limit: limit, Authority: mediaAuthority{epoch, revision}, Expires: expires}
		if token != "" {
			invalid := func() (Result, error) { return Result{}, model.TextError(model.ErrorCursorInvalid, nil) }
			parts := strings.Split(token, ".")
			if len(token) > 4096 || len(parts) != 3 || parts[0] != "tp1" {
				return invalid()
			}
			sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
			if err != nil || base64.RawURLEncoding.EncodeToString(sig) != parts[2] || !hmac.Equal(sig, s.mediaMAC("topic-cursor-v1", parts[1])) {
				return invalid()
			}
			payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
			if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
				return invalid()
			}
			decoded, err := model.DecodeStrict[topicCursor](payload)
			if err != nil {
				return invalid()
			}
			canonical, _ := json.Marshal(decoded)
			if string(canonical) != string(payload) || decoded.Peer != peer || decoded.Limit != limit || decoded.Authority != cursor.Authority || decoded.Expires > expires || decoded.Position.Date <= 0 || decoded.Position.Topic <= 0 || decoded.Position.Message <= 0 {
				return invalid()
			}
			cursor = decoded
			expires = cursor.Expires
		}
		if err := check(); err != nil {
			return Result{}, err
		}
		page, err := backend.Topics(ctx, peer, cursor.Position, limit)
		if err != nil {
			return Result{}, err
		}
		if len(page.Items) > limit {
			return Result{}, model.TextError(model.ErrorResultTooLarge, nil)
		}
		items = page.Items
		if page.Next != nil {
			if *page.Next == cursor.Position || page.Next.Date <= 0 || page.Next.Message <= 0 || page.Next.Topic <= 0 || page.Next.Date > math.MaxInt32 || page.Next.Message > math.MaxInt32 || page.Next.Topic > math.MaxInt32 || len(items) == 0 {
				return Result{}, model.TextError(model.ErrorInvalidReference, nil)
			}
			cursor.Position = *page.Next
			encoded, err := json.Marshal(cursor)
			if err != nil {
				return Result{}, err
			}
			payload := base64.RawURLEncoding.EncodeToString(encoded)
			value := "tp1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.mediaMAC("topic-cursor-v1", payload))
			next = &value
		}
	} else {
		if token != "" {
			return Result{}, model.TextError(model.ErrorCursorInvalid, nil)
		}
		all, err := lease.List(ctx)
		if err != nil {
			return Result{}, err
		}
		for _, g := range all {
			if g.Peer.Parent() == peer && g.Peer.TopicID() != 0 && (g.Profile != policy.ProfileSelfAuthored || g.Author == s.backend.SelfID()) {
				grants = append(grants, g)
			}
		}
		// Restricted discovery is the complete bounded grant set, not a remote forum scan.
		for _, g := range grants {
			if err := check(); err != nil {
				return Result{}, err
			}
			item, err := backend.Topic(ctx, g.Peer)
			if err != nil {
				return Result{}, err
			}
			if item.ID != g.Peer {
				return Result{}, model.TextError(model.ErrorInvalidReference, nil)
			}
			items = append(items, item)
		}
	}
	seen := map[model.PeerID]bool{}
	for _, item := range items {
		if item.ID.Parent() != peer || item.ID.TopicID() == 0 || seen[item.ID] || item.UnreadCount < 0 || item.UnreadCount > math.MaxInt32 || !utf8.ValidString(item.Title) || len(item.Title) > 4096 || (item.Hidden && item.ID.TopicID() != 1) {
			return Result{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		seen[item.ID] = true
	}
	if err := check(); err != nil {
		return Result{}, err
	}
	result, err = prepare(requestID, items, s.now(), false, model.ReadEffect{Kind: model.ReadEffectNone}, next, nil)
	if err != nil {
		return Result{}, err
	}
	count = len(items)
	return result, nil
}
