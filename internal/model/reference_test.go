package model

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestPeerIDRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []string{
		"tgpeer:v1:self:1",
		"tgpeer:v1:user:834726192",
		"tgpeer:v1:chat:482719",
		"tgpeer:v1:channel:1948273619",
		"tgpeer:v1:user:9007199254740992",
		"tgpeer:v1:user:9223372036854775807",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			parsed, err := ParsePeerID(input)
			if err != nil {
				t.Fatalf("ParsePeerID() error = %v", err)
			}
			if got := parsed.String(); got != input {
				t.Fatalf("String() = %q, want %q", got, input)
			}
			encoded, err := json.Marshal(parsed)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if string(encoded) != `"`+input+`"` {
				t.Fatalf("Marshal() = %s", encoded)
			}
			var decoded PeerID
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if decoded != parsed {
				t.Fatalf("decoded = %#v, want %#v", decoded, parsed)
			}
		})
	}
}

func TestPeerIDRejectsNonCanonicalValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"tgpeer",
		"tgpeer:v2:user:1",
		"tgpeer:v1:unknown:1",
		"tgpeer:v1:user:",
		"tgpeer:v1:user:0",
		"tgpeer:v1:user:01",
		"tgpeer:v1:user:+1",
		"tgpeer:v1:user:-1",
		"tgpeer:v1:user: 1",
		"tgpeer:v1:user:١",
		"tgpeer:v1:user:9223372036854775808",
		"tgpeer:v1:user:1:extra",
		strings.Repeat("x", maximumReferenceLength+1),
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := ParsePeerID(input)
			if !errors.Is(err, ErrInvalidReference) {
				t.Fatalf("ParsePeerID() error = %v, want ErrInvalidReference", err)
			}
		})
	}
}

func TestPeerIDJSONRejectsNumbersAndZeroValue(t *testing.T) {
	t.Parallel()

	var id PeerID
	if err := json.Unmarshal([]byte(`9007199254740992`), &id); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Unmarshal(number) error = %v", err)
	}
	if _, err := json.Marshal(PeerID{}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Marshal(zero) error = %v", err)
	}
}

func TestMessageIDRoundTrip(t *testing.T) {
	t.Parallel()

	input := "tgmsg:v1:channel:1948273619:2147483647"
	parsed, err := ParseMessageID(input)
	if err != nil {
		t.Fatalf("ParseMessageID() error = %v", err)
	}
	if parsed.String() != input {
		t.Fatalf("String() = %q, want %q", parsed.String(), input)
	}
	if parsed.TelegramID() != math.MaxInt32 {
		t.Fatalf("TelegramID() = %d", parsed.TelegramID())
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(encoded) != `"`+input+`"` {
		t.Fatalf("Marshal() = %s", encoded)
	}
	var decoded MessageID
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded != parsed {
		t.Fatalf("decoded = %#v, want %#v", decoded, parsed)
	}
}

func TestMessageIDRejectsNonCanonicalValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"tgmsg:v2:user:1:1",
		"tgmsg:v1:unknown:1:1",
		"tgmsg:v1:user:0:1",
		"tgmsg:v1:user:1:0",
		"tgmsg:v1:user:1:01",
		"tgmsg:v1:user:1:+1",
		"tgmsg:v1:user:1:2147483648",
		"tgmsg:v1:user:1:1:extra",
		strings.Repeat("x", maximumReferenceLength+1),
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := ParseMessageID(input)
			if !errors.Is(err, ErrInvalidReference) {
				t.Fatalf("ParseMessageID() error = %v, want ErrInvalidReference", err)
			}
		})
	}
}

func FuzzPeerIDRoundTrip(f *testing.F) {
	f.Add("tgpeer:v1:user:1")
	f.Add("tgpeer:v1:channel:9007199254740992")
	f.Add("not-a-reference")
	f.Fuzz(func(t *testing.T, input string) {
		parsed, err := ParsePeerID(input)
		if err != nil {
			return
		}
		if parsed.String() != input {
			t.Fatalf("accepted reference changed on round trip: %q -> %q", input, parsed.String())
		}
	})
}

func FuzzMessageIDRoundTrip(f *testing.F) {
	f.Add("tgmsg:v1:user:1:1")
	f.Add("tgmsg:v1:channel:1948273619:4827")
	f.Add("not-a-reference")
	f.Fuzz(func(t *testing.T, input string) {
		parsed, err := ParseMessageID(input)
		if err != nil {
			return
		}
		if parsed.String() != input {
			t.Fatalf("accepted reference changed on round trip: %q -> %q", input, parsed.String())
		}
	})
}
