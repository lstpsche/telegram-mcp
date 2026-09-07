package reader

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type scopeCursorBinding struct {
	FilenameDigest     string                `json:"filename_digest,omitempty"`
	UnreadMentionsOnly bool                  `json:"unread_mentions_only,omitempty"`
	Sender             string                `json:"sender,omitempty"`
	MediaType          model.SearchMediaType `json:"media_type,omitempty"`
	PinnedOnly         bool                  `json:"pinned_only,omitempty"`
	Since              int64                 `json:"since,omitempty"`
	Until              int64                 `json:"until,omitempty"`
	Operation          string                `json:"operation"`
	Scope              model.ScopeID         `json:"scope"`
	MembersDigest      string                `json:"members_digest"`
	QueryDigest        string                `json:"query_digest"`
	Limit              int                   `json:"limit"`
	Epoch              string                `json:"epoch"`
	Revision           int64                 `json:"revision"`
}

type scopeSearchCursor struct {
	Binding scopeCursorBinding `json:"binding"`
	Index   int                `json:"index"`
	Before  int32              `json:"before"`
	Ceiling int32              `json:"ceiling"`
	Expires int64              `json:"expires"`
}

func (s *Service) scopeMembersDigest(grants []policy.Grant) string {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte("scope-members-v1\x00"))
	for _, grant := range grants {
		mac.Write([]byte(grant.Peer.String()))
		mac.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) scopeCursorSignature(payload string) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte("scope-search-cursor-v1\x00"))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (s *Service) encodeScopeCursor(cursor scopeSearchCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", model.TextError(model.ErrorInternal, err)
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	return "ss1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.scopeCursorSignature(payload)), nil
}

func (s *Service) decodeScopeCursor(token string, binding scopeCursorBinding) (scopeSearchCursor, error) {
	invalid := func() (scopeSearchCursor, error) {
		return scopeSearchCursor{}, model.TextError(model.ErrorCursorInvalid, nil)
	}
	if len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "ss1" {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[2] || !hmac.Equal(signature, s.scopeCursorSignature(parts[1])) {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return invalid()
	}
	cursor, err := model.DecodeStrict[scopeSearchCursor](payload)
	if err != nil {
		return invalid()
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(payload, canonical) || cursor.Binding != binding || cursor.Index < 0 || cursor.Index >= policy.MaximumScopePeers || cursor.Before < 0 || cursor.Ceiling <= 0 || cursor.Ceiling < cursor.Before {
		return invalid()
	}
	now := s.now()
	if cursor.Expires <= now.Unix() {
		return scopeSearchCursor{}, model.TextError(model.ErrorCursorExpired, nil)
	}
	if cursor.Expires > now.Add(cursorLifetime).Unix() {
		return invalid()
	}
	return cursor, nil
}
