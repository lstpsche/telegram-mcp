package reader

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestFilenameSearchUsesPermittedMetadataAndCandidateProgress(t *testing.T) {
	s, f, p, _, g := testService(t)
	g.Documents = true
	saveGrant(t, p, g)
	items := mediaSearchCandidates(g)
	document := items[2]
	document.Document.Filename = "../Отчёт.PDF"
	f.items = []model.Candidate{candidate(g, 25, "Отчёт.PDF caption"), document}
	filter := model.SearchFilter{FilenameQuery: "  ОТЧЁТ  "}
	result, err := s.Search(context.Background(), "req_filename", g.Peer, filter, 2, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 1 || page.Items[0].Document.Filename != "../Отчёт.PDF" || page.NextCursor == nil || f.ackCalls != 0 {
		t.Fatal("filename selector mismatch")
	}
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(*page.NextCursor, ".")[1])
	if strings.Contains(string(payload), "Отчёт") || strings.Contains(string(payload), "отчёт") {
		t.Fatal("filename query escaped into cursor")
	}
	result, err = s.Search(context.Background(), "req_changed_filename", g.Peer, model.SearchFilter{FilenameQuery: "different"}, 2, *page.NextCursor)
	if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 {
		t.Fatal("filename cursor binding missing", err)
	}
	g.Documents = false
	saveGrant(t, p, g)
	result, err = s.Search(context.Background(), "req_filename_denied", g.Peer, filter, 2, "")
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 0 || page.NextCursor == nil {
		t.Fatal("denied metadata exposed or candidate progress lost")
	}
}

func TestFilenameScopeAndBatchSearch(t *testing.T) {
	s, f, p, _, grants, scope := scopeService(t)
	for _, g := range grants {
		g.Documents = true
		saveGrant(t, p, g)
		c := mediaSearchCandidates(g)[2]
		c.Document.Filename = "report.pdf"
		f.messages[g.Peer] = []model.Candidate{c}
	}
	filter := model.SearchFilter{FilenameQuery: "REPORT", MediaType: model.SearchMediaPDF}
	result, err := s.SearchScope(context.Background(), "req_filename_scope", scope.ID, filter, 1, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatal("scoped filename search failed")
	}
	calls := len(f.queries)
	_, err = s.SearchScope(context.Background(), "req_filename_scope_changed", scope.ID, model.SearchFilter{FilenameQuery: "other", MediaType: model.SearchMediaPDF}, 1, *page.NextCursor)
	if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(f.queries) != calls {
		t.Fatal("scope binding failed")
	}
	result, err = s.Searches(context.Background(), "req_filename_batch", []model.SearchRequest{{Peer: grants[0].Peer, Filter: filter, Limit: 1}, {Scope: scope.ID, Filter: filter, Limit: 1}})
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 1 || len(page.Searches) != 2 {
		t.Fatal("batch filename deduplication failed")
	}
}

func TestRenamedDocumentInvalidatesExistingHandle(t *testing.T) {
	s, f, _, _, token := documentService(t, "application/pdf")
	f.items[0].Document.Filename = "changed.pdf"
	result, err := s.OpenDocument(context.Background(), "req_renamed", token)
	if model.TextErrorCategory(err) != model.ErrorInvalidReference || len(result.JSON) != 0 || f.downloads != 0 || f.ackCalls != 0 {
		t.Fatal("renamed source retained old handle", err)
	}
}
