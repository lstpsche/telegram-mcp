package reader

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

const cursorLifetime = 15 * time.Minute

type cursorBinding struct {
	Operation   string       `json:"operation"`
	Peer        model.PeerID `json:"peer"`
	QueryDigest string       `json:"query_digest"`
	Limit       int          `json:"limit"`
	Epoch       string       `json:"epoch"`
	Revision    int64        `json:"revision"`
}

type searchCursor struct {
	Binding cursorBinding `json:"binding"`
	Before  int32         `json:"before"`
	Ceiling int32         `json:"ceiling"`
	Expires int64         `json:"expires"`
}

func (s *Service) queryDigest(query string) string {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte("search-query-v1\x00"))
	mac.Write([]byte(query))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) cursorSignature(payload string) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte("search-cursor-v1\x00"))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (s *Service) encodeCursor(cursor searchCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", model.TextError(model.ErrorInternal, err)
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	return "sc1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.cursorSignature(payload)), nil
}

func (s *Service) decodeCursor(token string, binding cursorBinding) (searchCursor, error) {
	invalid := func() (searchCursor, error) { return searchCursor{}, model.TextError(model.ErrorCursorInvalid, nil) }
	if len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "sc1" {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[2] || !hmac.Equal(signature, s.cursorSignature(parts[1])) {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return invalid()
	}
	cursor, err := model.DecodeStrict[searchCursor](payload)
	if err != nil {
		return invalid()
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(payload, canonical) || cursor.Binding != binding || cursor.Before <= 0 || cursor.Ceiling < cursor.Before {
		return invalid()
	}
	now := s.now()
	if cursor.Expires <= now.Unix() {
		return searchCursor{}, model.TextError(model.ErrorCursorExpired, nil)
	}
	if cursor.Expires > now.Add(cursorLifetime).Unix() {
		return invalid()
	}
	return cursor, nil
}
