package policy

import (
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestBroadcastGrantBindsPublisher(t *testing.T) {
	now := time.Now()
	publisher := peer(t, model.PeerKindChannel, 42)
	id, _ := model.NewMessageID(publisher, 10)
	g := Grant{Peer: publisher, Author: publisher, Profile: ProfileConsented, MinID: 1, MaxID: 20, ReadThrough: 20, Eligible: true, ExpiresAt: now.Add(time.Hour)}
	c := model.Candidate{Message: model.Message{ID: id, Author: publisher, ChannelPost: &model.ChannelPost{Sender: "tgpeer:v1:user:7"}, Text: "post"}}
	self := peer(t, model.PeerKindUser, 7)
	if err := g.Validate(now); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckMessage(c, self, now); err != nil {
		t.Fatal(err)
	}
	if err := fullReadGrant(publisher).CheckMessage(c, self, now); err != nil {
		t.Fatal(err)
	}
	g.Profile = ProfileSelfAuthored
	if g.Validate(now) == nil || g.CheckMessage(c, self, now) == nil {
		t.Fatal("self-authored channel authority")
	}
	g.Profile = ProfileConsented
	g.Author = self
	if g.CheckMessage(c, self, now) == nil {
		t.Fatal("display sender authorized channel")
	}
	g.Author = publisher
	c.Message.ChannelPost = nil
	if g.CheckMessage(c, self, now) == nil {
		t.Fatal("anonymous group message authorized")
	}
	c.Message.ChannelPost = &model.ChannelPost{}
	c.Message.Author = peer(t, model.PeerKindChannel, 99)
	if g.CheckMessage(c, self, now) == nil {
		t.Fatal("different channel authorized")
	}
}
