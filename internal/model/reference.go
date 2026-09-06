// Package model contains Telegram MCP's transport-independent, versioned public
// contracts. It intentionally contains no gotd generated types.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	referenceVersion       = "v1"
	maximumReferenceLength = 128
)

var ErrInvalidReference = errors.New("invalid Telegram reference")

// PeerKind disambiguates Telegram ID spaces that otherwise overlap.
type PeerKind string

const (
	PeerKindSelf    PeerKind = "self"
	PeerKindUser    PeerKind = "user"
	PeerKindChat    PeerKind = "chat"
	PeerKindChannel PeerKind = "channel"
)

func (k PeerKind) IsValid() bool {
	switch k {
	case PeerKindSelf, PeerKindUser, PeerKindChat, PeerKindChannel:
		return true
	default:
		return false
	}
}

// PeerID is a kinded, immutable Telegram peer reference. Its JSON form is a
// string so identifiers never lose precision in JavaScript clients.
type PeerID struct {
	kind  PeerKind
	id    uint64
	topic uint32
}

func NewPeerID(kind PeerKind, telegramID int64) (PeerID, error) {
	if !kind.IsValid() {
		return PeerID{}, referenceError("peer", "unknown kind")
	}
	if telegramID <= 0 {
		return PeerID{}, referenceError("peer", "identifier must be positive")
	}
	return PeerID{kind: kind, id: uint64(telegramID)}, nil
}

func ParsePeerID(value string) (PeerID, error) {
	if len(value) == 0 || len(value) > maximumReferenceLength {
		return PeerID{}, referenceError("peer", "invalid length")
	}
	parts := strings.Split(value, ":")
	if (len(parts) != 4 && len(parts) != 6) || parts[0] != "tgpeer" || parts[1] != referenceVersion {
		return PeerID{}, referenceError("peer", "invalid shape or version")
	}

	peer, err := parsePeerParts(parts[2], parts[3], "peer")
	if err != nil {
		return PeerID{}, err
	}
	if len(parts) == 6 {
		topic, err := parseCanonicalPositive(parts[5], math.MaxInt32)
		if err != nil || parts[4] != "topic" {
			return PeerID{}, referenceError("peer", "invalid topic")
		}
		return NewTopicPeer(peer, int32(topic))
	}
	return peer, nil
}

// NewTopicPeer identifies one topic inside a forum. Parent authority is distinct.
func NewTopicPeer(parent PeerID, topic int32) (PeerID, error) {
	if !parent.valid() || parent.kind != PeerKindChannel || parent.topic != 0 || topic <= 0 {
		return PeerID{}, referenceError("peer", "invalid topic")
	}
	parent.topic = uint32(topic)
	return parent, nil
}
func (id PeerID) TopicID() int32 { return int32(id.topic) }
func (id PeerID) Parent() PeerID {
	id.topic = 0
	return id
}

func (id PeerID) Kind() PeerKind { return id.kind }

func (id PeerID) TelegramID() int64 { return int64(id.id) }

func (id PeerID) valid() bool {
	return id.kind.IsValid() && id.id > 0 && id.id <= math.MaxInt64 && (id.topic == 0 || (id.kind == PeerKindChannel && id.topic <= math.MaxInt32))
}

func (id PeerID) String() string {
	if !id.valid() {
		return ""
	}
	value := "tgpeer:" + referenceVersion + ":" + string(id.kind) + ":" + strconv.FormatUint(id.id, 10)
	if id.topic != 0 {
		value += ":topic:" + strconv.FormatUint(uint64(id.topic), 10)
	}
	return value
}

func (id PeerID) MarshalText() ([]byte, error) {
	if !id.valid() {
		return nil, referenceError("peer", "zero or invalid value")
	}
	return []byte(id.String()), nil
}

func (id *PeerID) UnmarshalText(text []byte) error {
	parsed, err := ParsePeerID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id PeerID) MarshalJSON() ([]byte, error) {
	text, err := id.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (id *PeerID) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || len(data) > maximumReferenceLength+2 || data[0] != '"' {
		return referenceError("peer", "JSON value must be a string")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return referenceError("peer", "invalid JSON string")
	}
	return id.UnmarshalText([]byte(value))
}

// MessageID binds a Telegram message identifier to the peer whose history
// defines its meaning.
type MessageID struct {
	peer      PeerID
	messageID uint32
}

func NewMessageID(peer PeerID, telegramMessageID int32) (MessageID, error) {
	if !peer.valid() {
		return MessageID{}, referenceError("message", "invalid peer")
	}
	if telegramMessageID <= 0 {
		return MessageID{}, referenceError("message", "identifier must be positive")
	}
	return MessageID{peer: peer, messageID: uint32(telegramMessageID)}, nil
}

func ParseMessageID(value string) (MessageID, error) {
	if len(value) == 0 || len(value) > maximumReferenceLength {
		return MessageID{}, referenceError("message", "invalid length")
	}
	parts := strings.Split(value, ":")
	if (len(parts) != 5 && len(parts) != 7) || parts[0] != "tgmsg" || parts[1] != referenceVersion {
		return MessageID{}, referenceError("message", "invalid shape or version")
	}

	peer, err := ParsePeerID("tgpeer:" + strings.Join(parts[1:len(parts)-1], ":"))
	if err != nil {
		return MessageID{}, referenceError("message", "invalid peer")
	}
	messageID, err := parseCanonicalPositive(parts[len(parts)-1], math.MaxInt32)
	if err != nil {
		return MessageID{}, referenceError("message", err.Error())
	}
	return MessageID{peer: peer, messageID: uint32(messageID)}, nil
}

func (id MessageID) Peer() PeerID { return id.peer }

func (id MessageID) TelegramID() int32 { return int32(id.messageID) }

func (id MessageID) valid() bool {
	return id.peer.valid() && id.messageID > 0 && uint64(id.messageID) <= math.MaxInt32
}

func (id MessageID) String() string {
	if !id.valid() {
		return ""
	}
	return "tgmsg:" + strings.TrimPrefix(id.peer.String(), "tgpeer:") + ":" + strconv.FormatUint(uint64(id.messageID), 10)
}

func (id MessageID) MarshalText() ([]byte, error) {
	if !id.valid() {
		return nil, referenceError("message", "zero or invalid value")
	}
	return []byte(id.String()), nil
}

func (id *MessageID) UnmarshalText(text []byte) error {
	parsed, err := ParseMessageID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id MessageID) MarshalJSON() ([]byte, error) {
	text, err := id.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (id *MessageID) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || len(data) > maximumReferenceLength+2 || data[0] != '"' {
		return referenceError("message", "JSON value must be a string")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return referenceError("message", "invalid JSON string")
	}
	return id.UnmarshalText([]byte(value))
}

func parseCanonicalPositive(value string, maximum uint64) (uint64, error) {
	if value == "" {
		return 0, errors.New("identifier is empty")
	}
	if len(value) > 1 && value[0] == '0' {
		return 0, errors.New("identifier has a leading zero")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, errors.New("identifier is not canonical decimal")
		}
	}
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 || id > maximum {
		return 0, errors.New("identifier is outside its allowed range")
	}
	return id, nil
}

func parsePeerParts(kindValue, idValue, referenceType string) (PeerID, error) {
	kind := PeerKind(kindValue)
	if !kind.IsValid() {
		return PeerID{}, referenceError(referenceType, "unknown peer kind")
	}
	id, err := parseCanonicalPositive(idValue, math.MaxInt64)
	if err != nil {
		return PeerID{}, referenceError(referenceType, err.Error())
	}
	return PeerID{kind: kind, id: id}, nil
}

func referenceError(referenceType, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidReference, referenceType, reason)
}
