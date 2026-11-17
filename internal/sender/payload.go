package sender

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"io"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

// payloadDigest binds an operation to exact bytes and fixed sending semantics.
// Neither this digest nor an unkeyed content hash is a public identifier.
func (s *Service) payloadDigest(request model.SendRequest) [32]byte {
	mac := hmac.New(sha256.New, s.integrityKey[:])
	mac.Write([]byte("telegram-mcp/send-payload/v1\x00"))
	writeField(mac, request.Peer.String())
	writeField(mac, request.Text)
	if request.ReplyTo == nil {
		writeField(mac, "")
	} else {
		writeField(mac, request.ReplyTo.String())
	}
	writeField(mac, "plain-text/no-paid-messages/explicit-sender/v1")
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func writeField(mac hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	mac.Write(length[:])
	mac.Write([]byte(value))
}

func newRandomID(random io.Reader) (int64, error) {
	var data [8]byte
	for range 4 {
		if _, err := io.ReadFull(random, data[:]); err != nil {
			return 0, model.TextError(model.ErrorInternal, err)
		}
		if id := int64(binary.BigEndian.Uint64(data[:])); id != 0 {
			return id, nil
		}
	}
	return 0, model.TextError(model.ErrorInternal, nil)
}
