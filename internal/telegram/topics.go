package telegram

import (
	"context"
	"math"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func forumGroup(group *tg.Channel) bool {
	if group == nil || !group.Forum {
		return false
	}
	copy := *group
	copy.Forum = false
	return ordinarySupergroup(&copy)
}

func validTopic(peer model.PeerID, topic *tg.ForumTopic) bool {
	return topic != nil && !topic.Short && !topic.TitleMissing && topic.ID > 0 && topic.ID <= math.MaxInt32 && matchesPeer(peer, topic.Peer) && topic.Date > 0 && topic.TopMessage > 0 && topic.TopMessage <= math.MaxInt32 && topic.ReadInboxMaxID >= 0 && topic.ReadInboxMaxID <= math.MaxInt32 && topic.UnreadCount >= 0 && topic.UnreadCount <= math.MaxInt32 && (!topic.Hidden || topic.ID == 1) && utf8.ValidString(topic.Title) && len(topic.Title) <= 4096
}

func validateTopicResponse(peer model.PeerID, response *tg.MessagesForumTopics, limit int) error {
	if response == nil || response.Pts <= 0 || response.Count < 0 || len(response.Topics) > limit || len(response.Messages) > 100 || len(response.Users) > 200 || len(response.Chats) > 200 {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	matched := false
	for _, c := range response.Chats {
		if c.GetID() == peer.TelegramID() {
			group, ok := c.(*tg.Channel)
			if !ok || !forumGroup(group) {
				return model.TextError(model.ErrorUnsupportedPeer, nil)
			}
			matched = true
		}
	}
	if !matched && (len(response.Topics) != 0 || len(response.Chats) != 0) {
		return model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	return nil
}

func (a *Account) topic(ctx context.Context, peer model.PeerID) (*tg.ForumTopic, error) {
	if peer.TopicID() <= 0 {
		return nil, model.TextError(model.ErrorInvalidInput, nil)
	}
	input, err := a.reads.inputPeer(ctx, peer)
	if err != nil {
		return nil, err
	}
	response, err := a.reads.api.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: input, Topics: []int{int(peer.TopicID())}})
	if err != nil {
		return nil, readError(err)
	}
	if err := validateTopicResponse(peer, response, 1); err != nil {
		return nil, err
	}
	if len(response.Topics) != 1 {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	topic, ok := response.Topics[0].(*tg.ForumTopic)
	if !ok || !validTopic(peer, topic) || topic.ID != int(peer.TopicID()) {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := a.reads.synchronize(ctx); err != nil {
		return nil, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return topic, nil
}

func topicModel(peer model.PeerID, topic *tg.ForumTopic) model.Topic {
	return model.Topic{ID: peer, Title: topic.Title, Closed: topic.Closed, Hidden: topic.Hidden, UnreadCount: topic.UnreadCount}
}

func (a *Account) Topic(ctx context.Context, peer model.PeerID) (model.Topic, error) {
	if !a.Ready() {
		return model.Topic{}, model.TextError(model.ErrorNotReady, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	chat, err := a.Chat(ctx, peer.Parent())
	if err != nil {
		return model.Topic{}, err
	}
	if !chat.Forum {
		return model.Topic{}, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	t, err := a.topic(ctx, peer)
	if err != nil {
		return model.Topic{}, err
	}
	return topicModel(peer, t), nil
}

func (a *Account) Topics(ctx context.Context, peer model.PeerID, position model.TopicPosition, limit int) (model.TopicPage, error) {
	if !a.Ready() {
		return model.TopicPage{}, model.TextError(model.ErrorNotReady, nil)
	}
	if peer.Kind() != model.PeerKindChannel || peer.TopicID() != 0 || model.ValidatePageSize(limit) != nil || position.Date < 0 || position.Message < 0 || position.Topic < 0 || position.Date > math.MaxInt32 || position.Message > math.MaxInt32 || position.Topic > math.MaxInt32 {
		return model.TopicPage{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	chat, err := a.Chat(ctx, peer)
	if err != nil {
		return model.TopicPage{}, err
	}
	if !chat.Forum {
		return model.TopicPage{}, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	input, err := a.reads.inputPeer(ctx, peer)
	if err != nil {
		return model.TopicPage{}, err
	}
	response, err := a.reads.api.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{Peer: input, OffsetDate: position.Date, OffsetID: position.Message, OffsetTopic: position.Topic, Limit: limit})
	if err != nil {
		return model.TopicPage{}, readError(err)
	}
	if err := validateTopicResponse(peer, response, limit); err != nil {
		return model.TopicPage{}, err
	}
	// A terminal page may omit all entities. Refresh the forum independently so
	// absence of topic rows cannot hide a concurrent protection or type change.
	if len(response.Topics) == 0 && len(response.Chats) == 0 {
		current, err := a.Chat(ctx, peer)
		if err != nil {
			return model.TopicPage{}, err
		}
		if !current.Forum {
			return model.TopicPage{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
	}

	page := model.TopicPage{Items: []model.Topic{}}
	seen := map[int]bool{}
	var last *tg.ForumTopic
	for _, v := range response.Topics {
		t, ok := v.(*tg.ForumTopic)
		if !ok || !validTopic(peer, t) || seen[t.ID] {
			return model.TopicPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		seen[t.ID] = true
		id, err := model.NewTopicPeer(peer, int32(t.ID))
		if err != nil {
			return model.TopicPage{}, err
		}
		page.Items = append(page.Items, topicModel(id, t))
		last = t
	}
	if last != nil {
		date := last.Date
		if !response.OrderByCreateDate {
			date = 0
			for _, v := range response.Messages {
				if v.GetID() == last.TopMessage {
					switch m := v.(type) {
					case *tg.Message:
						if matchesPeer(peer, m.PeerID) {
							date = m.Date
						}
					case *tg.MessageService:
						if matchesPeer(peer, m.PeerID) {
							date = m.Date
						}
					}
				}
			}
			if date <= 0 {
				return model.TopicPage{}, model.TextError(model.ErrorInvalidReference, nil)
			}
		}
		next := model.TopicPosition{Date: date, Message: last.TopMessage, Topic: last.ID}
		if next == position {
			return model.TopicPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		// A short page is not terminal: Telegram chooses its own page size.
		page.Next = &next
	}
	if err := a.reads.synchronize(ctx); err != nil {
		return model.TopicPage{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return page, nil
}

// Fetch only the selected thread, including General (topic 1).
func (r *readRuntime) historyPage(ctx context.Context, peer model.PeerID, q *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error) {
	if peer.TopicID() != 0 {
		return r.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{Peer: q.Peer, MsgID: int(peer.TopicID()), OffsetID: q.OffsetID, OffsetDate: q.OffsetDate, AddOffset: q.AddOffset, Limit: q.Limit, MinID: q.MinID, MaxID: q.MaxID})
	}
	return r.api.MessagesGetHistory(ctx, q)
}

func (a *Account) acknowledgeTopic(ctx context.Context, peer model.PeerID, input tg.InputPeerClass, through int32) error {
	if _, err := a.reads.api.MessagesReadDiscussion(ctx, &tg.MessagesReadDiscussionRequest{Peer: input, MsgID: int(peer.TopicID()), ReadMaxID: int(through)}); err != nil {
		return readError(err)
	}
	topic, err := a.topic(ctx, peer)
	if err != nil {
		return err
	}
	if topic.ReadInboxMaxID < int(through) {
		return model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	return nil
}

// DiscoverTopics owns a bounded human metadata observation, with no grants or receipts.
func (a *Account) DiscoverTopics(ctx context.Context, peer model.PeerID, position model.TopicPosition) (page model.TopicPage, err error) {
	err = a.Observe(ctx, func(ctx context.Context, status AuthorizationStatus) error {
		if !status.Authorized {
			return model.TextError(model.ErrorNotReady, nil)
		}
		var err error
		page, err = a.Topics(ctx, peer, position, 100)
		return err
	})
	if err != nil {
		return model.TopicPage{}, err
	}
	return page, nil
}
