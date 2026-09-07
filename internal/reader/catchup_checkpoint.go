package reader

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

const catchUpCheckpointLifetime = 30 * 24 * time.Hour

type catchUpCheckpoint struct {
	Scope         model.ScopeID `json:"scope"`
	MembersDigest string        `json:"members_digest"`
	Epoch         string        `json:"epoch"`
	Revision      int64         `json:"revision"`
	Until         int64         `json:"until"`
	Expires       int64         `json:"expires"`
}

// CatchUpFrom resumes after a completely traversed window. A checkpoint is
// client-held navigation, reauthorized against the current scope on every call.
func (s *Service) CatchUpFrom(ctx context.Context, requestID string, scope model.ScopeID, checkpoint string, until int64, limit int, cursor string) (Result, error) {
	if checkpoint == "" || len(checkpoint) > 4096 || until <= 0 || until > math.MaxInt32 {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	return s.searchScope(ctx, requestID, scope, model.SearchFilter{}, limit, cursor, &model.DateWindow{Until: until}, checkpoint)
}

func (s *Service) checkpointSignature(payload string) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte("catch-up-checkpoint-v1\x00"))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (s *Service) encodeCatchUpCheckpoint(checkpoint catchUpCheckpoint) (string, error) {
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return "", model.TextError(model.ErrorInternal, err)
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	return "cu1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.checkpointSignature(payload)), nil
}

func (s *Service) decodeCatchUpCheckpoint(token string, binding scopeCursorBinding) (catchUpCheckpoint, error) {
	invalid := func() (catchUpCheckpoint, error) {
		return catchUpCheckpoint{}, model.TextError(model.ErrorCursorInvalid, nil)
	}
	if len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "cu1" {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[2] || !hmac.Equal(signature, s.checkpointSignature(parts[1])) {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return invalid()
	}
	checkpoint, err := model.DecodeStrict[catchUpCheckpoint](payload)
	if err != nil {
		return invalid()
	}
	canonical, err := json.Marshal(checkpoint)
	if err != nil || !bytes.Equal(payload, canonical) || checkpoint.Scope != binding.Scope || checkpoint.MembersDigest != binding.MembersDigest || checkpoint.Epoch != binding.Epoch || checkpoint.Revision != binding.Revision || checkpoint.Until <= 0 || checkpoint.Until > math.MaxInt32 || checkpoint.Until > s.now().Unix() {
		return invalid()
	}
	if checkpoint.Expires <= s.now().Unix() {
		return catchUpCheckpoint{}, model.TextError(model.ErrorCursorExpired, nil)
	}
	if checkpoint.Expires > s.now().Add(catchUpCheckpointLifetime).Unix() {
		return invalid()
	}
	return checkpoint, nil
}
