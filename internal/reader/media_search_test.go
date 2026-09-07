package reader

import (
	"context"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func mediaSearchCandidates(g policy.Grant) []model.Candidate {
	sources := []model.MediaSource{
		{Kind: "photo", MIMEType: "image/jpeg", Width: 32, Height: 32, Size: 100},
		{Kind: "document", MIMEType: "image/png", Width: 32, Height: 32, Size: 100},
		{Kind: "document", MIMEType: "application/pdf", Size: 100},
		{Kind: "document", MIMEType: "text/plain", Size: 100},
		{Kind: "voice", MIMEType: "audio/ogg", Size: 100, Duration: 1},
	}
	items := make([]model.Candidate, 0, len(sources))
	for i, source := range sources {
		source.Fingerprint = strings.Repeat("a", 64)
		c := candidate(g, 25-int32(i), "")
		c.Message.Pinned = true
		switch i {
		case 0, 1:
			c.Image = &source
		case 2, 3:
			c.Document = &source
		case 4:
			c.Voice = &source
		}
		items = append(items, c)
	}
	return items
}

func TestMediaSearchMatchesOnlyPermittedAttachments(t *testing.T) {
	for _, mode := range []string{"restricted", "full", "denied"} {
		for i, kind := range []model.SearchMediaType{model.SearchMediaPhoto, model.SearchMediaImageFile, model.SearchMediaPDF, model.SearchMediaTextFile, model.SearchMediaVoiceNote} {
			t.Run(mode+"/"+string(kind), func(t *testing.T) {
				s, f, p, _, g := testService(t)
				g.Images, g.Documents, g.VoiceNotes = mode != "denied", mode != "denied", mode != "denied"
				if mode == "full" {
					setFullRead(t, p, true)
				} else {
					saveGrant(t, p, g)
				}
				f.items = append(mediaSearchCandidates(g), candidate(g, 20, "ordinary text"))
				unpinned := f.items[i]
				unpinned.Message.ID, _ = model.NewMessageID(g.Peer, 19)
				unpinned.Message.Pinned = false
				f.items = append(f.items, unpinned)
				filter := model.SearchFilter{MediaType: kind, PinnedOnly: true}
				result, err := s.Search(context.Background(), "req_media", g.Peer, filter, 10, "")
				page := searchEnvelope(t, result, err)
				expected := 1
				if mode == "denied" {
					expected = 0
				}
				if len(page.Items) != expected || !page.Partial || page.NextCursor != nil || f.ackCalls != 0 || f.searchQuery.MediaType != kind || f.searchQuery.Query != "" || !f.searchQuery.PinnedOnly {
					t.Fatal("media discovery contract", err)
				}
				if expected == 1 && page.Items[0].ID != f.items[i].Message.ID {
					t.Fatal("wrong attachment selected")
				}
			})
		}
	}
}

func TestMediaScopeSearchContinuesAfterNonmatchingDocument(t *testing.T) {
	s, f, p, _, grants, scope := scopeService(t)
	for i := range grants {
		grants[i].Documents = true
		saveGrant(t, p, grants[i])
	}
	f.messages[grants[0].Peer] = []model.Candidate{mediaSearchCandidates(grants[0])[3]}
	pin := mediaSearchCandidates(grants[1])[2]
	pin.Message.ID, _ = model.NewMessageID(grants[1].Peer, 10)
	f.messages[grants[1].Peer] = []model.Candidate{pin}
	filter := model.SearchFilter{MediaType: model.SearchMediaPDF}
	result, err := s.SearchScope(context.Background(), "req_first", scope.ID, filter, 1, "")
	first := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(first.Items) != 0 || !first.Partial || first.NextCursor == nil {
		t.Fatal("nonmatching media lost continuation")
	}
	result, err = s.SearchScope(context.Background(), "req_next", scope.ID, filter, 1, *first.NextCursor)
	last := decodeScopeEnvelope[model.SearchHit](t, result, err)
	if len(last.Items) != 1 || last.Items[0].ID != pin.Message.ID || last.NextCursor != nil || last.Scope.CompletedPeers != 2 || f.ackCalls != 0 {
		t.Fatal("media scope traversal failed")
	}
	for _, q := range f.queries {
		if q.MediaType != model.SearchMediaPDF || q.Query != "" || q.Window != nil {
			t.Fatal("media selector lost")
		}
	}
}

func TestMediaSearchCursorBindsType(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		for _, change := range [][2]model.SearchMediaType{{"", model.SearchMediaPDF}, {model.SearchMediaPDF, ""}, {model.SearchMediaPDF, model.SearchMediaTextFile}} {
			s, f, p, _, grants, scope := scopeService(t)
			g := grants[0]
			g.Documents = true
			saveGrant(t, p, g)
			f.messages[g.Peer] = []model.Candidate{mediaSearchCandidates(g)[2]}
			filter := model.SearchFilter{Query: "q", MediaType: change[0]}
			search := func(token string) (Result, error) {
				if scoped {
					return s.SearchScope(context.Background(), "req_page", scope.ID, filter, 1, token)
				}
				return s.Search(context.Background(), "req_page", g.Peer, filter, 1, token)
			}
			result, err := search("")
			first := searchEnvelope(t, result, err)
			if first.NextCursor == nil {
				t.Fatal("fixture lacks cursor")
			}
			calls := len(f.queries)
			filter.MediaType = change[1]
			result, err = search(*first.NextCursor)
			if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 || len(f.queries) != calls {
				t.Fatal("media change reached backend", err)
			}
		}
	}
}
