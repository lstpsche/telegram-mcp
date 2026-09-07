package telegram

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func (r *readRuntime) inputPeer(ctx context.Context, peer model.PeerID) (tg.InputPeerClass, error) {
	if err := r.storage.checkEpoch(ctx); err != nil {
		return nil, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	switch peer.Kind() {
	case model.PeerKindSelf:
		if peer.TelegramID() != r.self.Load() {
			return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		return &tg.InputPeerSelf{}, nil
	case model.PeerKindChat:
		if peer.TelegramID() <= 0 {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		return &tg.InputPeerChat{ChatID: peer.TelegramID()}, nil
	case model.PeerKindChannel:
		hash, found, err := r.storage.GetChannelAccessHash(ctx, r.self.Load(), peer.TelegramID())
		if err != nil {
			return nil, model.TextError(model.ErrorFreshnessDegraded, err)
		}
		if !found || hash == 0 {
			return nil, model.TextError(model.ErrorNotReady, errors.New("Telegram peer metadata is missing"))
		}
		return &tg.InputPeerChannel{ChannelID: peer.TelegramID(), AccessHash: hash}, nil
	case model.PeerKindUser:
		if peer.TelegramID() == r.self.Load() {
			return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		hash, found, err := r.storage.GetUserAccessHash(ctx, r.self.Load(), peer.TelegramID())
		if err != nil {
			return nil, model.TextError(model.ErrorFreshnessDegraded, err)
		}
		if !found || hash == 0 {
			return nil, model.TextError(model.ErrorNotReady, errors.New("Telegram peer metadata is missing"))
		}
		return &tg.InputPeerUser{UserID: peer.TelegramID(), AccessHash: hash}, nil
	default:
		return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
}

func readableUser(user *tg.User) bool {
	return user != nil && user.ID > 0 && !user.Deleted && !user.Min && !user.Restricted
}

func ordinaryChat(chat *tg.Chat) bool {
	return chat != nil && chat.ID > 0 && !chat.Deactivated && !chat.Left && chat.MigratedTo == nil && !chat.Flags.Has(6) && !chat.Noforwards
}

func validateDialogEntities(peer model.PeerID, users []tg.UserClass, chats []tg.ChatClass) error {
	// Revalidate the entity accompanying the result, including empty/forbidden
	// constructors, instead of relying on the earlier metadata lookup.
	matched := peer.Kind() == model.PeerKindSelf
	for _, value := range users {
		if peer.Kind() == model.PeerKindUser && value.GetID() == peer.TelegramID() {
			user, ok := value.(*tg.User)
			if !ok || !readableUser(user) {
				return model.TextError(model.ErrorUnsupportedPeer, nil)
			}
			matched = true
		}
	}
	for _, value := range chats {
		if peer.Kind() == model.PeerKindChannel && value.GetID() == peer.TelegramID() {
			group, ok := value.(*tg.Channel)
			if !ok || !((ordinarySupergroup(group) || ordinaryBroadcast(group)) && peer.TopicID() == 0 || forumGroup(group) && peer.TopicID() != 0) {
				return model.TextError(model.ErrorUnsupportedPeer, nil)
			}
			matched = true
		}
		if peer.Kind() == model.PeerKindChat && value.GetID() == peer.TelegramID() {
			chat, ok := value.(*tg.Chat)
			if !ok || !ordinaryChat(chat) {
				return model.TextError(model.ErrorUnsupportedPeer, nil)
			}
			matched = true
		}
	}
	if !matched {
		return model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	return nil
}

func (a *Account) Chat(ctx context.Context, peer model.PeerID) (model.Chat, error) {
	if !a.Ready() {
		return model.Chat{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	input, err := a.reads.inputPeer(bounded, peer)
	if err != nil {
		return model.Chat{}, err
	}
	var title string
	forum, broadcast := false, false
	switch input := input.(type) {
	case *tg.InputPeerSelf:
		title = "Saved Messages"
	case *tg.InputPeerUser:
		users, err := a.reads.api.UsersGetUsers(bounded, []tg.InputUserClass{&tg.InputUser{UserID: input.UserID, AccessHash: input.AccessHash}})
		if err != nil {
			return model.Chat{}, readError(err)
		}
		if len(users) != 1 {
			return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		user, ok := users[0].(*tg.User)
		if !ok || !readableUser(user) || user.ID != peer.TelegramID() {
			return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		title = strings.TrimSpace(user.FirstName + " " + user.LastName)
		if err := a.reads.saveUsers(bounded, users); err != nil {
			return model.Chat{}, err
		}
	case *tg.InputPeerChannel:
		result, err := a.reads.api.ChannelsGetChannels(bounded, []tg.InputChannelClass{&tg.InputChannel{ChannelID: input.ChannelID, AccessHash: input.AccessHash}})
		if err != nil {
			return model.Chat{}, readError(err)
		}
		groups := result.GetChats()
		if len(groups) != 1 {
			return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		group, ok := groups[0].(*tg.Channel)
		if !ok || group.ID != peer.TelegramID() || !(ordinarySupergroup(group) || ordinaryBroadcast(group) || forumGroup(group)) {
			return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		forum, broadcast = group.Forum, group.Broadcast
		if peer.TopicID() != 0 {
			if !forum {
				return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
			}
			topic, err := a.topic(bounded, peer)
			if err != nil {
				return model.Chat{}, err
			}
			title = topic.Title
		} else {
			title = group.Title
		}
		if err := a.reads.saveChannels(bounded, groups); err != nil {
			return model.Chat{}, err
		}
	case *tg.InputPeerChat:
		result, err := a.reads.api.MessagesGetChats(bounded, []int64{input.ChatID})
		if err != nil {
			return model.Chat{}, readError(err)
		}
		chats := result.GetChats()
		if len(chats) != 1 {
			return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		chat, ok := chats[0].(*tg.Chat)
		if !ok || !ordinaryChat(chat) || chat.ID != peer.TelegramID() {
			return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		title = chat.Title
	}
	if !utf8.ValidString(title) || len(title) > 4096 {
		return model.Chat{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	if err := a.reads.synchronize(bounded); err != nil {
		return model.Chat{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return model.Chat{ID: peer, Title: title, Broadcast: broadcast, Forum: forum && peer.TopicID() == 0}, nil
}

func (r *readRuntime) saveUsers(ctx context.Context, users []tg.UserClass) error {
	if len(users) > 200 {
		return model.TextError(model.ErrorResultTooLarge, nil)
	}
	for _, value := range users {
		user, ok := value.(*tg.User)
		if !ok || user.Min || user.ID <= 0 {
			continue
		}
		hash, ok := user.GetAccessHash()
		if !ok || hash == 0 {
			continue
		}
		if err := r.storage.SetUserAccessHash(ctx, r.self.Load(), user.ID, hash); err != nil {
			return model.TextError(model.ErrorFreshnessDegraded, err)
		}
	}
	return nil
}

type messagePage interface {
	GetMessages() []tg.MessageClass
	GetUsers() []tg.UserClass
	GetChats() []tg.ChatClass
	GetTopics() []tg.ForumTopicClass
}

func (r *readRuntime) normalizePage(ctx context.Context, peer model.PeerID, result tg.MessagesMessagesClass, limit int) ([]model.Candidate, error) {
	var page messagePage
	switch value := result.(type) {
	case *tg.MessagesMessages:
		if peer.Kind() == model.PeerKindChannel {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		page = value
	case *tg.MessagesMessagesSlice:
		if peer.Kind() == model.PeerKindChannel {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		page = value
	case *tg.MessagesChannelMessages:
		if peer.Kind() != model.PeerKindChannel || value.Pts <= 0 {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		page = value
	default:
		return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	if len(page.GetMessages()) > limit || len(page.GetChats()) > 200 || (peer.TopicID() == 0 && len(page.GetTopics()) != 0) || len(page.GetTopics()) > 100 {
		return nil, model.TextError(model.ErrorResultTooLarge, nil)
	}
	if err := r.saveUsers(ctx, page.GetUsers()); err != nil {
		return nil, err
	}
	authors := make(map[int64]bool, len(page.GetUsers()))
	for _, value := range page.GetUsers() {
		if user, ok := value.(*tg.User); ok {
			authors[user.ID] = readableUser(user)
		}
	}
	authors[r.self.Load()] = true
	if err := validateDialogEntities(peer, page.GetUsers(), page.GetChats()); err != nil {
		return nil, err
	}
	broadcast := false
	for _, value := range page.GetChats() {
		if channel, ok := value.(*tg.Channel); ok && channel.ID == peer.TelegramID() && peer.Kind() == model.PeerKindChannel {
			broadcast = ordinaryBroadcast(channel)
		}
	}
	candidates := make([]model.Candidate, 0, len(page.GetMessages()))
	seen := map[int]bool{}
	for _, value := range page.GetMessages() {
		if seen[value.GetID()] {
			return nil, model.TextError(model.ErrorTelegramUnavailable, errors.New("Telegram history contains duplicate IDs"))
		}
		seen[value.GetID()] = true
		candidate, err := normalizeMessage(peer, r.self.Load(), value, authors, broadcast)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func normalizeMessage(peer model.PeerID, self int64, value tg.MessageClass, authors map[int64]bool, broadcast bool) (model.Candidate, error) {
	if value.GetID() <= 0 || value.GetID() > math.MaxInt32 {
		return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	id, err := model.NewMessageID(peer, int32(value.GetID()))
	if err != nil {
		return model.Candidate{}, err
	}
	candidate := model.Candidate{Message: model.Message{ID: id}}
	isBroadcast := broadcast && peer.Kind() == model.PeerKindChannel && peer.TopicID() == 0
	message, ok := value.(*tg.Message)
	if !ok {
		candidate.Unsupported = true
		return candidate, nil
	}
	if !matchesPeer(peer, message.PeerID) {
		return model.Candidate{}, model.TextError(model.ErrorInvalidReference, errors.New("Telegram message dialog does not match request"))
	}
	candidate.Protected = message.Noforwards || len(message.RestrictionReason) > 0
	candidate.Ephemeral = message.TTLPeriod != 0
	candidate.Forwarded = !message.FwdFrom.Zero() || message.Flags.Has(2)
	var forward *model.Forward
	if candidate.Forwarded {
		forward = normalizeForward(message.FwdFrom)
	}
	for _, entity := range message.Entities {
		if _, ok := entity.(*tg.MessageEntityBlockquote); ok {
			candidate.Quoted = true
		}
	}
	// SavedPeerID groups notes and forwarded copies; it never selects authority.
	unsupportedSavedDialog := message.SavedPeerID != nil && (peer.Kind() != model.PeerKindSelf || peer.TelegramID() != self || (!candidate.Forwarded && !matchesPeer(peer, message.SavedPeerID)))
	candidate.Unsupported = (candidate.Forwarded && forward == nil) || (message.Post && !isBroadcast) || message.Legacy || message.Offline || message.FromScheduled || unsupportedSavedDialog || message.ViaBotID != 0 || message.ViaBusinessBotID != 0 || message.GuestchatViaFrom != nil || message.QuickReplyShortcutID != 0 || message.ReportDeliveryUntilDate != 0 || message.ScheduleRepeatPeriod != 0 || !message.RichMessage.Zero() || message.SummaryFromLanguage != ""
	var image, document, voice *model.MediaSource
	var poll *model.Poll
	var preview *model.LinkPreview
	if media, ok := message.Media.(*tg.MessageMediaPoll); ok {
		poll, err = normalizePoll(media)
		if err != nil {
			return model.Candidate{}, err
		}
		candidate.Unsupported = candidate.Unsupported || poll == nil || message.Mentioned && message.MediaUnread || message.VideoProcessingPending || message.PaidSuggestedPostStars || message.PaidSuggestedPostTon || message.PaidMessageStars != 0 || !message.SuggestedPost.Zero()
	} else if media, ok := message.Media.(*tg.MessageMediaWebPage); ok {
		preview, err = normalizeLinkPreview(media)
		if err != nil {
			return model.Candidate{}, err
		}
		candidate.Unsupported = candidate.Unsupported || message.Mentioned && message.MediaUnread || message.VideoProcessingPending || message.PaidSuggestedPostStars || message.PaidSuggestedPostTon || message.PaidMessageStars != 0 || !message.SuggestedPost.Zero()
	} else if message.Media != nil {
		if _, empty := message.Media.(*tg.MessageMediaEmpty); !empty {
			location := normalizeMedia(message)
			if location == nil {
				candidate.Unsupported = true
			} else if location.source.IsVoice() {
				voice = &location.source
			} else if location.source.IsDocument() {
				document = &location.source
			} else {
				image = &location.source
			}
		}
	}
	if message.GroupedID != 0 || message.Flags.Has(17) {
		candidate.Unsupported = candidate.Unsupported || message.GroupedID == 0 || (image == nil && document == nil && voice == nil)
	}
	if message.ReplyTo != nil {
		reply, ok := message.ReplyTo.(*tg.MessageReplyHeader)
		if !ok {
			candidate.Unsupported = true
		} else {
			candidate.Quoted = candidate.Quoted || reply.Quote || reply.QuoteText != "" || len(reply.QuoteEntities) > 0 || !reply.ReplyFrom.Zero() || reply.ReplyMedia != nil
			candidate.Unsupported = candidate.Unsupported || (peer.TopicID() == 0 && reply.ForumTopic) || reply.ReplyToPeerID != nil || reply.ReplyToScheduled
			candidate.Ephemeral = candidate.Ephemeral || reply.ReplyToEphemeral
			if reply.ReplyToMsgID != 0 || reply.Flags.Has(4) {
				if reply.ReplyToMsgID <= 0 || reply.ReplyToMsgID >= message.ID || reply.ReplyToMsgID > math.MaxInt32 {
					candidate.Unsupported = true
				} else {
					parent, err := model.NewMessageID(peer, int32(reply.ReplyToMsgID))
					if err != nil {
						return model.Candidate{}, err
					}
					candidate.Message.ReplyTo = &parent
					root, err := normalizeThreadRoot(peer, reply, isBroadcast)
					if err != nil {
						candidate.Unsupported = true
					} else {
						candidate.Message.ThreadRoot = root
					}
				}
			}
		}
	}
	if peer.TopicID() != 0 {
		topic := int32(1)
		if reply, ok := message.ReplyTo.(*tg.MessageReplyHeader); ok && !reply.ForumTopic && reply.ReplyToTopID != 0 {
			return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if reply, ok := message.ReplyTo.(*tg.MessageReplyHeader); ok && reply.ForumTopic {
			if reply.ReplyToTopID > 0 && reply.ReplyToTopID <= math.MaxInt32 {
				topic = int32(reply.ReplyToTopID)
			} else if reply.ReplyToMsgID > 0 && reply.ReplyToMsgID <= math.MaxInt32 {
				topic = int32(reply.ReplyToMsgID)
			} else {
				return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
			}
		}
		if topic != peer.TopicID() {
			return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
		}
	}
	var author int64
	if peer.Kind() == model.PeerKindSelf && candidate.Forwarded {
		author = self
	} else if from, ok := message.FromID.(*tg.PeerUser); ok {
		author = from.UserID
	} else if message.FromID == nil {
		switch {
		case message.Out:
			author = self
		case peer.Kind() == model.PeerKindUser:
			author = peer.TelegramID()
		case peer.Kind() == model.PeerKindSelf:
			author = self
		}
	}
	if author > 0 {
		candidate.Message.Author, _ = model.NewPeerID(model.PeerKindUser, author)
	}
	if isBroadcast {
		candidate.Message.Author = peer
		post := &model.ChannelPost{Signature: message.PostAuthor}
		if message.FromID != nil {
			var sender model.PeerID
			var err error
			switch from := message.FromID.(type) {
			case *tg.PeerUser:
				sender, err = model.NewPeerID(model.PeerKindUser, from.UserID)
			case *tg.PeerChannel:
				sender, err = model.NewPeerID(model.PeerKindChannel, from.ChannelID)
			default:
				err = model.ErrInvalidReference
			}
			if err != nil {
				candidate.Unsupported = true
			} else {
				post.Sender = sender.String()
			}
		}
		candidate.Message.ChannelPost = post
	} else {
		candidate.Unsupported = candidate.Unsupported || author <= 0 || !authors[author]
	}
	candidate.Unsupported = candidate.Unsupported || !candidate.Message.ValidAuthor() || message.Date <= 0 || (message.Message == "" && image == nil && document == nil && voice == nil && poll == nil && preview == nil)
	if !utf8.ValidString(message.Message) || len(message.Message) > 64*1024 {
		return model.Candidate{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	if !candidate.Protected && !candidate.Ephemeral && !candidate.Quoted && !candidate.Unsupported {
		if message.GroupedID != 0 {
			candidate.Message.AlbumID, err = model.NewAlbumID(peer, message.GroupedID)
			if err != nil {
				return model.Candidate{}, err
			}
		}
		if supplied, present := message.GetReactions(); present {
			candidate.Message.Reactions, err = normalizeReactions(peer, supplied)
			if err != nil {
				return model.Candidate{}, err
			}
		}
		if message.SavedPeerID != nil {
			source, err := normalizePeerID(message.SavedPeerID, self)
			if err != nil {
				return model.Candidate{}, model.TextError(model.ErrorInvalidReference, err)
			}
			candidate.Message.SavedPeer = source.String()
			if !candidate.Message.ValidSavedPeer() {
				return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
			}
		}
		if replies, present := message.GetReplies(); present && replies.Comments {
			if !isBroadcast || replies.ChannelID <= 0 || replies.ChannelID == peer.TelegramID() {
				return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
			}
			linked, err := model.NewPeerID(model.PeerKindChannel, replies.ChannelID)
			if err != nil {
				return model.Candidate{}, err
			}
			candidate.Message.DiscussionPeer = linked.String()
		}
		candidate.Message.Pinned = message.Pinned
		candidate.Message.LinkPreview = preview
		candidate.Message.Poll = poll
		candidate.Message.Forward = forward
		candidate.Message.Text = message.Message
		candidate.Message.Date = time.Unix(int64(message.Date), 0).UTC().Format(time.RFC3339)
		candidate.Image = image
		candidate.Document = document
		candidate.Voice = voice
	} else {
		candidate.Message.ChannelPost = nil
		candidate.Message.ReplyTo = nil
		candidate.Message.ThreadRoot = nil
	}
	return candidate, nil
}
func matchesPeer(peer model.PeerID, value tg.PeerClass) bool {
	switch value := value.(type) {
	case *tg.PeerUser:
		return (peer.Kind() == model.PeerKindUser || peer.Kind() == model.PeerKindSelf) && peer.TelegramID() == value.UserID
	case *tg.PeerChannel:
		return peer.Kind() == model.PeerKindChannel && peer.TelegramID() == value.ChannelID
	case *tg.PeerChat:
		return peer.Kind() == model.PeerKindChat && peer.TelegramID() == value.ChatID
	default:
		return false
	}
}

func (a *Account) History(ctx context.Context, q model.HistoryQuery) ([]model.Candidate, error) {
	if !a.Ready() {
		return nil, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if q.MinID <= 0 || q.MaxID < q.MinID || q.Limit < 1 || q.Limit > 100 || q.Before < 0 || q.Target < 0 || q.BeforeCount < 0 || q.AfterCount < 0 || q.BeforeCount > 99 || q.AfterCount > 99 || q.BeforeCount+q.AfterCount+1 > 100 {
		return nil, model.TextError(model.ErrorInvalidInput, nil)
	}
	if q.Target > 0 && (q.Target < q.MinID || q.Target > q.MaxID) {
		return nil, model.TextError(model.ErrorPolicyDenied, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	// Do not fetch a body until the current peer's class/protection is checked.
	if chat, err := a.Chat(bounded, q.Peer); err != nil {
		return nil, err
	} else if chat.Forum {
		return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	input, err := a.reads.inputPeer(bounded, q.Peer)
	if err != nil {
		return nil, err
	}
	fetch := func(offset, add, limit int) ([]model.Candidate, error) {
		maxID := 0
		if q.MaxID < math.MaxInt32 {
			maxID = int(q.MaxID) + 1
		}
		result, err := a.reads.historyPage(bounded, q.Peer, &tg.MessagesGetHistoryRequest{Peer: input, OffsetID: offset, AddOffset: add, Limit: limit, MinID: int(q.MinID) - 1, MaxID: maxID})
		if err != nil {
			return nil, readError(err)
		}
		return a.reads.normalizePage(bounded, q.Peer, result, limit)
	}
	var candidates []model.Candidate
	if q.Target == 0 {
		offset := int(q.Before)
		if q.MaxID < math.MaxInt32 && (offset == 0 || offset > int(q.MaxID)+1) {
			offset = int(q.MaxID) + 1
		}
		candidates, err = fetch(offset, 0, q.Limit)
	} else {
		maximum := 0
		if q.Target < math.MaxInt32 {
			maximum = int(q.Target) + 1
		}
		// Keep the lookup peer-scoped: global getMessages IDs could fetch a body
		// belonging to another dialog before its mismatch could be detected.
		result, fetchErr := a.reads.historyPage(bounded, q.Peer, &tg.MessagesGetHistoryRequest{Peer: input, OffsetID: int(q.Target), AddOffset: -1, Limit: 1, MinID: int(q.Target) - 1, MaxID: maximum})
		if fetchErr != nil {
			return nil, readError(fetchErr)
		}
		candidates, err = a.reads.normalizePage(bounded, q.Peer, result, 1)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			if err := a.reads.synchronize(bounded); err != nil {
				return nil, model.TextError(model.ErrorFreshnessDegraded, err)
			}
			return candidates, nil
		}
		if len(candidates) != 1 || candidates[0].Message.ID.TelegramID() != q.Target {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		target := candidates[0]
		if target.Unsupported || target.Protected || target.Ephemeral || target.Quoted {
			return candidates, nil
		}
		if q.BeforeCount > 0 {
			before, fetchErr := fetch(int(q.Target), 0, q.BeforeCount)
			if fetchErr != nil {
				return nil, fetchErr
			}
			for _, message := range before {
				if message.Message.ID.TelegramID() >= q.Target {
					return nil, model.TextError(model.ErrorInvalidReference, nil)
				}
			}
			candidates = append(candidates, before...)
		}
		if q.AfterCount > 0 {
			after, fetchErr := fetch(int(q.Target), -q.AfterCount-1, q.AfterCount+1)
			if fetchErr != nil {
				return nil, fetchErr
			}
			for _, message := range after {
				if message.Message.ID.TelegramID() > q.Target {
					candidates = append(candidates, message)
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	for _, message := range candidates {
		id := message.Message.ID.TelegramID()
		if id < q.MinID || id > q.MaxID || (q.Target == 0 && q.Before > 0 && id >= q.Before) {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
	}
	if len(candidates) > q.Limit {
		return nil, model.TextError(model.ErrorResultTooLarge, nil)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Message.ID.TelegramID() > candidates[j].Message.ID.TelegramID()
	})
	if err := a.reads.synchronize(bounded); err != nil {
		return nil, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return candidates, nil
}

// Discover is a bounded human control-plane operation. It returns supported
// metadata from the first main-folder page, never bodies or read acknowledgments.
func (a *Account) Discover(ctx context.Context) ([]model.Chat, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var chats []model.Chat
	err := a.Observe(bounded, func(ctx context.Context, status AuthorizationStatus) error {
		if !status.Authorized {
			return model.TextError(model.ErrorNotReady, nil)
		}
		page, err := a.Dialogs(ctx, model.DialogPosition{}, 100)
		if err != nil {
			return err
		}
		chats = make([]model.Chat, 0, len(page.Items))
		for _, entry := range page.Items {
			chats = append(chats, entry.Chat)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return chats, nil
}
