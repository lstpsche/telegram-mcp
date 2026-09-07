package reader

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

type discussionFake struct {
	*fakeBackend
	root    model.MessageID
	calls   int
	resolve func() error
}

func (f *discussionFake) Discussion(_ context.Context, _ model.MessageID, _ model.PeerID) (model.MessageID, error) {
	f.calls++
	if f.resolve != nil {
		if err := f.resolve(); err != nil {
			return model.MessageID{}, err
		}
	}
	return f.root, nil
}

func TestDiscussionResolutionRequiresFullReadAndWithholdsFailedResults(t *testing.T) {
	for _, mode := range []string{"success", "restricted", "missing_link", "wrong_root", "upstream", "relinked", "protected", "ack", "freshness"} {
		t.Run(mode, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			source, _ := model.NewPeerID(model.PeerKindChannel, 42)
			destination, _ := model.NewPeerID(model.PeerKindChannel, 99)
			root, _ := model.NewMessageID(destination, 100)
			g.Peer, g.Author = source, source
			c := candidate(g, 20, "synthetic post")
			c.Message.ChannelPost = &model.ChannelPost{}
			c.Message.DiscussionPeer = destination.String()
			f.items = []model.Candidate{c}
			b := &discussionFake{fakeBackend: f, root: root}
			s.backend = b
			if mode != "restricted" {
				setFullRead(t, p, true)
			}
			switch mode {
			case "missing_link":
				f.items[0].Message.DiscussionPeer = ""
			case "wrong_root":
				b.root, _ = model.NewMessageID(source, 100)
			case "upstream":
				b.resolve = func() error { return errors.New("synthetic transport failure") }
			case "relinked":
				b.resolve = func() error { f.items[0].Message.DiscussionPeer = "tgpeer:v1:channel:98"; return nil }
			case "protected":
				b.resolve = func() error { f.items[0].Protected = true; return nil }
			case "ack":
				f.ackError = errors.New("synthetic receipt failure")
			case "freshness":
				b.resolve = func() error { f.notReady = true; return nil }
			}
			result, err := s.Messages(context.Background(), "req_discussion", model.HistoryQuery{Peer: source, Target: 20, Limit: 1, ResolveDiscussion: true})
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				var page model.Envelope[model.Message]
				if err := json.Unmarshal(result.JSON, &page); err != nil {
					t.Fatal(err)
				}
				if len(page.Items) != 1 || page.Items[0].DiscussionRoot == nil || *page.Items[0].DiscussionRoot != root || f.ackCalls != 1 || f.through != 20 || f.historyCalls != 2 || b.calls != 1 {
					t.Fatal("discussion navigation or source receipt incorrect")
				}
			} else {
				if err == nil || len(result.JSON) != 0 {
					t.Fatal("failed resolution released content", err)
				}
				if mode != "ack" && f.ackCalls != 0 {
					t.Fatal("failure reached receipt")
				}
				if mode == "restricted" && (f.historyCalls != 0 || b.calls != 0) {
					t.Fatal("restricted resolution performed I/O")
				}
			}
		})
	}
}

func TestReplySearchFiltersBoundCursorsWithoutFetchingParents(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	root, _ := model.NewMessageID(g.Peer, 5) // Parent outside body grant is navigation only.
	parent, _ := model.NewMessageID(g.Peer, 15)
	first := candidate(g, 25, "nested reply")
	first.Message.ReplyTo, first.Message.ThreadRoot = &parent, &root
	other := candidate(g, 24, "other thread")
	other.Message.ReplyTo, other.Message.ThreadRoot = &parent, &parent
	f.items = []model.Candidate{first, other}
	filter := model.SearchFilter{ReplyTo: parent.String(), ThreadRoot: root.String()}
	result, err := s.Search(context.Background(), "req_replies", g.Peer, filter, 2, "")
	page := searchEnvelope(t, result, err)
	if len(page.Items) != 1 || page.NextCursor == nil || page.Items[0].ThreadRoot == nil || f.ackCalls != 0 || f.historyCalls != 1 || f.query.Target != 0 {
		t.Fatal("reply intersection or continuation incorrect")
	}
	for _, change := range []func(*model.SearchFilter){func(f *model.SearchFilter) { f.ReplyTo = root.String() }, func(f *model.SearchFilter) { f.ThreadRoot = parent.String() }} {
		changed := filter
		change(&changed)
		result, err = s.Search(context.Background(), "req_changed", g.Peer, changed, 2, *page.NextCursor)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(result.JSON) != 0 {
			t.Fatal("cursor escaped reply binding", err)
		}
	}
	f.items = []model.Candidate{other, candidate(g, 23, "unrelated")}
	result, err = s.Search(context.Background(), "req_empty", g.Peer, filter, 2, "")
	page = searchEnvelope(t, result, err)
	if len(page.Items) != 0 || page.NextCursor == nil {
		t.Fatal("empty filtered window lost cursor")
	}
	otherPeer, _ := model.NewPeerID(model.PeerKindChat, 77)
	result, err = s.Search(context.Background(), "req_cross", otherPeer, filter, 2, "")
	if model.TextErrorCategory(err) != model.ErrorInvalidInput || len(result.JSON) != 0 {
		t.Fatal("cross peer reply selector accepted")
	}
}

func TestThreadMutationWithholdsMedia(t *testing.T) {
	for _, when := range []string{"download", "receipt"} {
		t.Run(when, func(t *testing.T) {
			s, f, _, g, _ := imageService(t)
			parent, _ := model.NewMessageID(g.Peer, 15)
			root, _ := model.NewMessageID(g.Peer, 10)
			f.items[0].Message.ReplyTo = &parent
			f.items[0].Message.ThreadRoot = &root
			result, err := s.Search(context.Background(), "req_thread_image", g.Peer, model.SearchFilter{ThreadRoot: root.String()}, 20, "")
			page := searchEnvelope(t, result, err)
			if len(page.Items) != 1 || page.Items[0].Image == nil {
				t.Fatal("missing fixture image")
			}
			mutate := func() { *f.items[0].Message.ThreadRoot, _ = model.NewMessageID(g.Peer, 11) }
			if when == "download" {
				f.onDownload = mutate
			} else {
				f.onAck = mutate
			}
			result, err = s.OpenImage(context.Background(), "req_changed_thread", page.Items[0].Image.Handle)
			if err == nil || result.Image != nil || len(result.JSON) != 0 {
				t.Fatal("changed thread attribution escaped")
			}
			if when == "download" && f.ackCalls != 0 {
				t.Fatal("changed thread reached receipt")
			}
		})
	}
}
