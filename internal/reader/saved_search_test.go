package reader

import (
	"context"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func TestSavedSearchIntersectsSourceTagAndAuthor(t *testing.T) {
	s, f, p, _, g := testService(t)
	g.Peer, _ = model.NewPeerID(model.PeerKindSelf, g.Author.TelegramID())
	g.Profile = policy.ProfileConsented
	saveGrant(t, p, g)
	c := candidate(g, 25, "saved copy")
	c.Message.SavedPeer = "tgpeer:v1:channel:99"
	c.Forwarded = true
	c.Message.Forward = &model.Forward{Date: "2026-09-01T00:00:00Z", FromPeer: "tgpeer:v1:user:999"}
	tag := model.SavedTag{Kind: "emoji", Emoji: "📌"}
	c.Message.Reactions = &model.Reactions{AsTags: true, Counts: []model.ReactionCount{{Kind: tag.Kind, Emoji: tag.Emoji, Count: 1}}}
	f.items = []model.Candidate{c}
	filter := model.SearchFilter{SavedPeer: c.Message.SavedPeer, SavedTag: tag, Sender: g.Author.String()}
	result, err := s.Search(context.Background(), "req_saved", g.Peer, filter, 1, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 1 || page.Items[0].SavedPeer != c.Message.SavedPeer || page.NextCursor == nil || f.ackCalls != 0 || f.searchQuery.SavedTag != tag {
		t.Fatal("saved search contract", err)
	}
	token := *page.NextCursor
	for _, changed := range []model.SearchFilter{{SavedPeer: "tgpeer:v1:chat:99", SavedTag: tag, Sender: g.Author.String()}, {SavedPeer: c.Message.SavedPeer, SavedTag: model.SavedTag{Kind: "emoji", Emoji: "⭐"}, Sender: g.Author.String()}} {
		calls := f.historyCalls
		result, err = s.Search(context.Background(), "req_cursor", g.Peer, changed, 1, token)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || f.historyCalls != calls {
			t.Fatal("saved cursor changed filters", err)
		}
	}
	for _, mode := range []string{"ordinary_reaction", "missing_source", "other_source", "origin_sender", "protected", "self_authored"} {
		f.items = []model.Candidate{c}
		current := filter
		switch mode {
		case "ordinary_reaction":
			f.items[0].Message.Reactions = &model.Reactions{Counts: c.Message.Reactions.Counts}
		case "missing_source":
			f.items[0].Message.SavedPeer = ""
		case "other_source":
			f.items[0].Message.SavedPeer = "tgpeer:v1:chat:99"
		case "origin_sender":
			current.Sender = "tgpeer:v1:user:999"
		case "protected":
			f.items[0].Protected = true
		case "self_authored":
			g.Profile = policy.ProfileSelfAuthored
			saveGrant(t, p, g)
		}
		result, err = s.Search(context.Background(), "req_exclude", g.Peer, current, 1, "")
		page = searchEnvelope(t, result, err)
		if len(page.Items) != 0 || page.NextCursor == nil || f.ackCalls != 0 {
			t.Fatal("saved filter or policy bypass", mode, err)
		}
	}
}

func TestSavedSearchRejectsNonSelfAndScopesBeforeFetch(t *testing.T) {
	s, f, _, _, grants, scope := scopeService(t)
	filter := model.SearchFilter{SavedPeer: "tgpeer:v1:chat:99"}
	for _, scoped := range []bool{false, true} {
		var result Result
		var err error
		if scoped {
			result, err = s.SearchScope(context.Background(), "req_invalid", scope.ID, filter, 10, "")
		} else {
			result, err = s.Search(context.Background(), "req_invalid", grants[0].Peer, filter, 10, "")
		}
		if model.TextErrorCategory(err) != model.ErrorInvalidInput || len(result.JSON) != 0 || len(f.queries) != 0 {
			t.Fatal("invalid saved selector fetched", err)
		}
	}
}
