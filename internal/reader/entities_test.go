package reader

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestFormattingRetainsTextUnderContainingAuthorAuthority(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	c := candidate(g, 20, "😀 code")
	c.Message.Entities = []model.TextEntity{{Kind: "blockquote", Offset: 0, Length: 7}, {Kind: "code", Offset: 3, Length: 4}}
	f.items = []model.Candidate{c}
	result, err := s.Messages(context.Background(), "req_formatting", request(g))
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[model.Message]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 || envelope.Items[0].Text != c.Message.Text || len(envelope.Items[0].Entities) != 2 || f.ackCalls != 1 {
		t.Fatal("formatting changed body or receipt")
	}
}

func TestFormattingFailureWithholdsBodiesBeforeAcknowledgment(t *testing.T) {
	for _, large := range []bool{false, true} {
		s, f, p, _, g := testService(t)
		saveGrant(t, p, g)
		c := candidate(g, 20, "😀 body")
		if large {
			for i := 0; i < 64; i++ {
				c.Message.Entities = append(c.Message.Entities, model.TextEntity{Kind: "text_url", Offset: 0, Length: 2, URL: "https://example.invalid/" + strings.Repeat("x", 3900)})
			}
		} else {
			c.Message.Entities = []model.TextEntity{{Kind: "bold", Offset: 1, Length: 1}}
		}
		f.items = []model.Candidate{c}
		result, err := s.Messages(context.Background(), "req_bad_formatting", request(g))
		expected := model.ErrorInvalidReference
		if large {
			expected = model.ErrorResultTooLarge
		}
		if model.TextErrorCategory(err) != expected || len(result.JSON) != 0 || f.ackCalls != 0 {
			t.Fatal("unsafe formatting released", err)
		}
	}
}
