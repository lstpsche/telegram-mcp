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

func ordinaryUser(user *tg.User) bool {
	return user != nil && user.ID > 0 && !user.Bot && !user.Deleted && !user.Min && !user.Restricted
}
func ordinaryChat(chat *tg.Chat) bool {
	return chat != nil && chat.ID > 0 && !chat.Deactivated && !chat.Left && chat.MigratedTo.Zero() && !chat.Noforwards
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
		if !ok || !ordinaryUser(user) || user.ID != peer.TelegramID() {
			return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
		title = strings.TrimSpace(user.FirstName + " " + user.LastName)
		if err := a.reads.saveUsers(bounded, users); err != nil {
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
	return model.Chat{ID: peer, Title: title}, nil
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
		page = value
	case *tg.MessagesMessagesSlice:
		page = value
	default:
		return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	if len(page.GetMessages()) > limit || len(page.GetChats()) > 200 || len(page.GetTopics()) != 0 {
		return nil, model.TextError(model.ErrorResultTooLarge, nil)
	}
	if err := r.saveUsers(ctx, page.GetUsers()); err != nil {
		return nil, err
	}
	authors := make(map[int64]bool, len(page.GetUsers()))
	for _, value := range page.GetUsers() {
		if user, ok := value.(*tg.User); ok {
			authors[user.ID] = ordinaryUser(user)
		}
	}
	authors[r.self.Load()] = true
	for _, value := range page.GetChats() {
		if chat, ok := value.(*tg.Chat); ok && peer.Kind() == model.PeerKindChat && chat.ID == peer.TelegramID() && !ordinaryChat(chat) {
			return nil, model.TextError(model.ErrorProtectedContent, nil)
		}
	}
	candidates := make([]model.Candidate, 0, len(page.GetMessages()))
	seen := map[int]bool{}
	for _, value := range page.GetMessages() {
		if seen[value.GetID()] {
			return nil, model.TextError(model.ErrorTelegramUnavailable, errors.New("Telegram history contains duplicate IDs"))
		}
		seen[value.GetID()] = true
		candidate, err := normalizeMessage(peer, r.self.Load(), value, authors)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func normalizeMessage(peer model.PeerID, self int64, value tg.MessageClass, authors map[int64]bool) (model.Candidate, error) {
	if value.GetID() <= 0 || value.GetID() > math.MaxInt32 {
		return model.Candidate{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	id, err := model.NewMessageID(peer, int32(value.GetID()))
	if err != nil {
		return model.Candidate{}, err
	}
	candidate := model.Candidate{Message: model.Message{ID: id}}
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
	for _, entity := range message.Entities {
		if _, ok := entity.(*tg.MessageEntityBlockquote); ok {
			candidate.Quoted = true
		}
	}
	candidate.Unsupported = message.Post || message.Legacy || message.Offline || message.FromScheduled || message.SavedPeerID != nil || message.ViaBotID != 0 || message.ViaBusinessBotID != 0 || message.GuestchatViaFrom != nil || message.ReplyMarkup != nil || message.QuickReplyShortcutID != 0 || message.ReportDeliveryUntilDate != 0 || message.ScheduleRepeatPeriod != 0 || !message.RichMessage.Zero() || message.SummaryFromLanguage != ""
	if message.Media != nil {
		if _, empty := message.Media.(*tg.MessageMediaEmpty); !empty {
			candidate.Unsupported = true
		}
	}
	if message.ReplyTo != nil {
		reply, ok := message.ReplyTo.(*tg.MessageReplyHeader)
		if !ok {
			candidate.Unsupported = true
		} else {
			candidate.Quoted = candidate.Quoted || reply.Quote || reply.QuoteText != "" || len(reply.QuoteEntities) > 0 || !reply.ReplyFrom.Zero() || reply.ReplyMedia != nil
			candidate.Unsupported = candidate.Unsupported || reply.ForumTopic || reply.ReplyToPeerID != nil || reply.ReplyToTopID != 0 || reply.ReplyToScheduled
			candidate.Ephemeral = candidate.Ephemeral || reply.ReplyToEphemeral
		}
	}
	var author int64
	if from, ok := message.FromID.(*tg.PeerUser); ok {
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
	candidate.Unsupported = candidate.Unsupported || author <= 0 || !authors[author] || message.Date <= 0 || message.Message == ""
	if !utf8.ValidString(message.Message) || len(message.Message) > 64*1024 {
		return model.Candidate{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	if !candidate.Protected && !candidate.Ephemeral && !candidate.Forwarded && !candidate.Quoted && !candidate.Unsupported {
		candidate.Message.Text = message.Message
		candidate.Message.Date = time.Unix(int64(message.Date), 0).UTC().Format(time.RFC3339)
	}
	return candidate, nil
}
func matchesPeer(peer model.PeerID, value tg.PeerClass) bool {
	switch value := value.(type) {
	case *tg.PeerUser:
		return (peer.Kind() == model.PeerKindUser || peer.Kind() == model.PeerKindSelf) && peer.TelegramID() == value.UserID
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
	if _, err := a.Chat(bounded, q.Peer); err != nil {
		return nil, err
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
		result, err := a.reads.api.MessagesGetHistory(bounded, &tg.MessagesGetHistoryRequest{Peer: input, OffsetID: offset, AddOffset: add, Limit: limit, MinID: int(q.MinID) - 1, MaxID: maxID})
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
		result, fetchErr := a.reads.api.MessagesGetHistory(bounded, &tg.MessagesGetHistoryRequest{Peer: input, OffsetID: int(q.Target), AddOffset: -1, Limit: 1, MinID: int(q.Target) - 1, MaxID: maximum})
		if fetchErr != nil {
			return nil, readError(fetchErr)
		}
		candidates, err = a.reads.normalizePage(bounded, q.Peer, result, 1)
		if err != nil {
			return nil, err
		}
		if len(candidates) != 1 || candidates[0].Message.ID.TelegramID() != q.Target {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		target := candidates[0]
		if target.Unsupported || target.Protected || target.Ephemeral || target.Forwarded || target.Quoted {
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

// Discover is a bounded human control-plane operation. Dialog RPCs may carry
// top-message bodies; only supported dialog names and IDs leave this adapter.
func (a *Account) Discover(ctx context.Context) ([]model.Chat, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var chats []model.Chat
	err := a.Observe(bounded, func(ctx context.Context, status AuthorizationStatus) error {
		if !status.Authorized || !a.Ready() {
			return model.TextError(model.ErrorNotReady, nil)
		}
		result, err := a.reads.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100})
		if err != nil {
			return readError(err)
		}
		var users []tg.UserClass
		var groups []tg.ChatClass
		var dialogs []tg.DialogClass
		switch value := result.(type) {
		case *tg.MessagesDialogs:
			users = value.Users
			groups = value.Chats
			dialogs = value.Dialogs
		case *tg.MessagesDialogsSlice:
			users = value.Users
			groups = value.Chats
			dialogs = value.Dialogs
		default:
			return model.TextError(model.ErrorTelegramUnavailable, nil)
		}
		if len(dialogs) > 100 || len(groups) > 200 {
			return model.TextError(model.ErrorResultTooLarge, nil)
		}
		if err := a.reads.saveUsers(ctx, users); err != nil {
			return err
		}
		supported := map[model.PeerID]string{}
		self, _ := model.NewPeerID(model.PeerKindSelf, a.reads.self.Load())
		supported[self] = "Saved Messages"
		for _, value := range users {
			user, ok := value.(*tg.User)
			if !ok || !ordinaryUser(user) || user.ID == a.reads.self.Load() {
				continue
			}
			if hash, ok := user.GetAccessHash(); !ok || hash == 0 {
				continue
			}
			id, _ := model.NewPeerID(model.PeerKindUser, user.ID)
			supported[id] = strings.TrimSpace(user.FirstName + " " + user.LastName)
		}
		for _, value := range groups {
			group, ok := value.(*tg.Chat)
			if !ok || !ordinaryChat(group) {
				continue
			}
			id, _ := model.NewPeerID(model.PeerKindChat, group.ID)
			supported[id] = group.Title
		}
		chats = make([]model.Chat, 0, len(dialogs))
		seen := map[model.PeerID]bool{}
		for _, value := range dialogs {
			dialog, ok := value.(*tg.Dialog)
			if !ok {
				continue
			}
			var id model.PeerID
			switch peer := dialog.Peer.(type) {
			case *tg.PeerUser:
				kind := model.PeerKindUser
				if peer.UserID == a.reads.self.Load() {
					kind = model.PeerKindSelf
				}
				id, _ = model.NewPeerID(kind, peer.UserID)
			case *tg.PeerChat:
				id, _ = model.NewPeerID(model.PeerKindChat, peer.ChatID)
			default:
				continue
			}
			title, ok := supported[id]
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			if !utf8.ValidString(title) || len(title) > 4096 {
				return model.TextError(model.ErrorResultTooLarge, nil)
			}
			chats = append(chats, model.Chat{ID: id, Title: title})
		}
		return a.reads.synchronize(ctx)
	})
	if err != nil {
		return nil, err
	}
	return chats, nil
}
