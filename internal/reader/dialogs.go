package reader

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type dialogBackend interface {
	Dialogs(context.Context, model.DialogPosition, int) (model.DialogPage, error)
}

type dialogCursor struct {
	QueryDigest string               `json:"query_digest,omitempty"`
	Operation   string               `json:"operation"`
	Epoch       string               `json:"epoch"`
	Revision    int64                `json:"revision"`
	Limit       int                  `json:"limit"`
	Position    model.DialogPosition `json:"position"`
	Expires     int64                `json:"expires"`
}

func (s *Service) dialogSignature(payload string) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte("dialog-cursor-v1\x00"))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (s *Service) encodeDialogCursor(cursor dialogCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", model.TextError(model.ErrorInternal, err)
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	return "dc1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.dialogSignature(payload)), nil
}

func (s *Service) decodeDialogCursor(token string, binding dialogCursor) (dialogCursor, error) {
	invalid := func() (dialogCursor, error) { return dialogCursor{}, model.TextError(model.ErrorCursorInvalid, nil) }
	if len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "dc1" {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[2] || !hmac.Equal(signature, s.dialogSignature(parts[1])) {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return invalid()
	}
	cursor, err := model.DecodeStrict[dialogCursor](payload)
	if err != nil {
		return invalid()
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(payload, canonical) || cursor.QueryDigest != binding.QueryDigest || cursor.Operation != binding.Operation || cursor.Epoch != binding.Epoch || cursor.Revision != binding.Revision || cursor.Limit != binding.Limit || !cursor.Position.Valid() || cursor.Expires > binding.Expires {
		return invalid()
	}
	if cursor.Expires <= s.now().Unix() {
		return dialogCursor{}, model.TextError(model.ErrorCursorExpired, nil)
	}
	return cursor, nil
}

// fullDialogs is entered with verified full authority and the policy lease held
// through caller audit and final validity. Each page performs one bounded lookup.
func (s *Service) fullDialogs(ctx context.Context, lease *policy.Lease, requestID, operation string, limit int, token string, expiry *int64, query string) (Result, int, error) {
	backend, ok := s.backend.(dialogBackend)
	if !ok {
		return Result{}, 0, model.TextError(model.ErrorNotReady, nil)
	}
	epoch, revision, err := lease.Binding(ctx)
	if err != nil {
		return Result{}, 0, err
	}
	cursor := dialogCursor{Operation: operation, Epoch: epoch, Revision: revision, Limit: limit, Expires: s.now().Add(cursorLifetime).Unix()}
	if query != "" {
		cursor.QueryDigest = s.queryDigest("discovery-title-v1\x00" + query)
	}
	if token != "" {
		cursor, err = s.decodeDialogCursor(token, cursor)
		if err != nil {
			return Result{}, 0, err
		}
	}
	*expiry = cursor.Expires
	page, err := backend.Dialogs(ctx, cursor.Position, limit)
	if err != nil {
		return Result{}, 0, err
	}
	if page.Scanned < len(page.Items) || page.Scanned > limit || page.Scanned < 0 {
		return Result{}, 0, model.TextError(model.ErrorResultTooLarge, nil)
	}
	chats := make([]model.Chat, 0, len(page.Items))
	unread := make([]model.Unread, 0, len(page.Items))
	seen := make(map[model.PeerID]bool)
	for _, entry := range page.Items {
		if entry.Chat.ID.String() == "" || seen[entry.Chat.ID] || entry.Unread.Peer != entry.Chat.ID || entry.Unread.Count < 0 || entry.Unread.Count > 2147483647 || !utf8.ValidString(entry.Chat.Title) || len(entry.Chat.Title) > 4096 {
			return Result{}, 0, model.TextError(model.ErrorInvalidReference, nil)
		}
		if entry.Chat.ID.Kind() == model.PeerKindSelf && entry.Chat.ID.TelegramID() != s.backend.SelfID().TelegramID() {
			return Result{}, 0, model.TextError(model.ErrorInvalidReference, nil)
		}
		seen[entry.Chat.ID] = true
		if strings.Contains(strings.ToLower(entry.Chat.Title), query) {
			chats = append(chats, entry.Chat)
		}
		if entry.Unread.Count > 0 || entry.Unread.Marked {
			unread = append(unread, entry.Unread)
		}
	}
	if err := s.checkGrantsCurrent(ctx, nil); err != nil {
		return Result{}, 0, err
	}
	if cursor.Expires <= s.now().Unix() {
		return Result{}, 0, model.TextError(model.ErrorCursorExpired, nil)
	}
	var next *string
	if page.Next != nil {
		if !page.Next.Valid() || *page.Next == cursor.Position {
			return Result{}, 0, model.TextError(model.ErrorInvalidReference, nil)
		}
		cursor.Position = *page.Next
		value, err := s.encodeDialogCursor(cursor)
		if err != nil {
			return Result{}, 0, err
		}
		next = &value
	}
	partial := page.Scanned > len(page.Items) || next != nil
	if operation == "list_unread" {
		result, err := prepare(requestID, unread, s.now(), partial, model.NoReadEffect(), next, nil)
		return result, len(unread), err
	}
	result, err := prepare(requestID, chats, s.now(), partial, model.NoReadEffect(), next, nil)
	return result, len(chats), err
}

func (s *Service) checkDialogRelease(ctx context.Context, expiry int64) error {
	if err := s.checkGrantsCurrent(ctx, nil); err != nil {
		return err
	}
	if expiry != 0 && expiry <= s.now().Unix() {
		return model.TextError(model.ErrorCursorExpired, nil)
	}
	return nil
}
