package reader

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

const observationLifetime = 24 * time.Hour

type messageObservation struct {
	Message   model.MessageID `json:"message"`
	Digest    string          `json:"digest"`
	Authority mediaAuthority  `json:"authority"`
	Expires   int64           `json:"expires"`
}

// Refresh compares current exact-message reads with client-held observations.
// Missing or inaccessible targets fail; they are never inferred to be deleted.
func (s *Service) Refresh(ctx context.Context, requestID string, tokens []string) (Result, error) {
	if len(tokens) == 0 || len(tokens) > model.MaximumContextTargets {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	queries := make([]model.HistoryQuery, 0, len(tokens))
	observations := make([]messageObservation, 0, len(tokens))
	for _, token := range tokens {
		observation, err := s.decodeObservation(token)
		if err != nil {
			return Result{}, err
		}
		observations = append(observations, observation)
		queries = append(queries, model.HistoryQuery{Peer: observation.Message.Peer(), Target: observation.Message.TelegramID(), Limit: 1})
	}
	return s.messages(ctx, requestID, queries, true, observations)
}

func (s *Service) decodeObservation(token string) (messageObservation, error) {
	invalid := func() (messageObservation, error) {
		return messageObservation{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	if len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "mo1" {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[2] || !hmac.Equal(signature, s.mediaMAC("message-observation-v1", parts[1])) {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return invalid()
	}
	observation, err := model.DecodeStrict[messageObservation](payload)
	if err != nil {
		return invalid()
	}
	canonical, err := json.Marshal(observation)
	if err != nil || !bytes.Equal(canonical, payload) || observation.Message.String() == "" || len(observation.Digest) != 43 || observation.Authority.Epoch == "" || observation.Authority.Revision < 0 || observation.Expires > s.now().Add(observationLifetime).Unix() {
		return invalid()
	}
	if observation.Expires <= s.now().Unix() {
		return messageObservation{}, model.TextError(model.ErrorResourceExpired, nil)
	}
	return observation, nil
}

func (s *Service) encodeObservation(observation messageObservation) (string, error) {
	data, err := json.Marshal(observation)
	if err != nil {
		return "", model.TextError(model.ErrorInternal, err)
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	return "mo1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.mediaMAC("message-observation-v1", payload)), nil
}

func (s *Service) observationDigest(message model.Message) (string, error) {
	message.Observation = ""
	message.RefreshState = ""
	message.AlbumContext = nil
	message.ReplyChain = nil
	message.DiscussionRoot = nil
	// Stable source digests detect replaced media even when display metadata matches.
	// Temporary signed handles would otherwise make every fresh read look changed.
	if message.Image != nil {
		value := *message.Image
		handle, err := s.decodeMediaHandle(value.Handle, mediaImage)
		if err != nil {
			return "", err
		}
		value.Handle = handle.Digest
		message.Image = &value
	}
	if message.Document != nil {
		value := *message.Document
		handle, err := s.decodeMediaHandle(value.Handle, mediaDocument)
		if err != nil {
			return "", err
		}
		value.Handle = handle.Digest
		message.Document = &value
	}
	if message.Voice != nil {
		value := *message.Voice
		handle, err := s.decodeMediaHandle(value.Handle, mediaVoice)
		if err != nil {
			return "", err
		}
		value.Handle = handle.Digest
		message.Voice = &value
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return "", model.TextError(model.ErrorInternal, err)
	}
	return base64.RawURLEncoding.EncodeToString(s.mediaMAC("message-observation-content-v1", string(encoded))), nil
}

func (s *Service) observeMessages(items []model.Message, grants []policy.Grant, authority mediaAuthority, previous []messageObservation) error {
	byPeer := make(map[model.PeerID]policy.Grant, len(grants))
	for _, g := range grants {
		byPeer[g.Peer] = g
	}
	old := make(map[model.MessageID]string, len(previous))
	for _, observation := range previous {
		old[observation.Message] = observation.Digest
	}
	for i := range items {
		item := &items[i]
		digest, err := s.observationDigest(*item)
		if err != nil {
			return err
		}
		grant, ok := byPeer[item.ID.Peer()]
		if !ok {
			return model.TextError(model.ErrorInvalidReference, nil)
		}
		item.Observation, err = s.encodeObservation(messageObservation{Message: item.ID, Digest: digest, Authority: authority, Expires: grant.Deadline(s.now().Add(observationLifetime)).Unix()})
		if err != nil {
			return err
		}
		item.RefreshState = ""
		if previous, ok := old[item.ID]; ok {
			item.RefreshState = "changed"
			if hmac.Equal([]byte(previous), []byte(digest)) {
				item.RefreshState = "unchanged"
			}
		}
	}
	return nil
}
