package telegram

import (
	"strconv"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func normalizeTextEntities(text string, input []tg.MessageEntityClass) ([]model.TextEntity, error) {
	if len(input) > model.MaximumTextEntities {
		return nil, model.TextError(model.ErrorResultTooLarge, nil)
	}
	if len(input) == 0 {
		return nil, nil
	}
	entities := make([]model.TextEntity, 0, len(input))
	for _, value := range input {
		if value == nil || value.GetLength() <= 0 {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		e := model.TextEntity{Offset: value.GetOffset(), Length: value.GetLength()}
		switch v := value.(type) {
		case *tg.MessageEntityUnknown:
			e.Kind = "unknown"
		case *tg.MessageEntityMention:
			e.Kind = "mention"
		case *tg.MessageEntityHashtag:
			e.Kind = "hashtag"
		case *tg.MessageEntityBotCommand:
			e.Kind = "bot_command"
		case *tg.MessageEntityURL:
			e.Kind = "url"
		case *tg.MessageEntityEmail:
			e.Kind = "email"
		case *tg.MessageEntityBold:
			e.Kind = "bold"
		case *tg.MessageEntityItalic:
			e.Kind = "italic"
		case *tg.MessageEntityCode:
			e.Kind = "code"
		case *tg.MessageEntityPre:
			e.Kind = "pre"
			e.Language = v.Language
		case *tg.MessageEntityTextURL:
			e.Kind = "text_url"
			e.URL = v.URL
		case *tg.MessageEntityMentionName:
			e.Kind = "mention_name"
			peer, err := model.NewPeerID(model.PeerKindUser, v.UserID)
			if err != nil {
				return nil, model.TextError(model.ErrorInvalidReference, err)
			}
			e.User = peer.String()
		case *tg.MessageEntityPhone:
			e.Kind = "phone"
		case *tg.MessageEntityCashtag:
			e.Kind = "cashtag"
		case *tg.MessageEntityUnderline:
			e.Kind = "underline"
		case *tg.MessageEntityStrike:
			e.Kind = "strike"
		case *tg.MessageEntityBankCard:
			e.Kind = "bank_card"
		case *tg.MessageEntitySpoiler:
			e.Kind = "spoiler"
		case *tg.MessageEntityCustomEmoji:
			e.Kind = "custom_emoji"
			e.CustomEmojiID = strconv.FormatInt(v.DocumentID, 10)
		case *tg.MessageEntityBlockquote:
			e.Kind = "blockquote"
			e.Collapsed = v.Collapsed
		case *tg.MessageEntityFormattedDate:
			e.Kind = "formatted_date"
			date := int64(v.Date)
			e.Date = &date
			for _, flag := range []struct {
				name    string
				enabled bool
			}{{"relative", v.Relative}, {"short_time", v.ShortTime}, {"long_time", v.LongTime}, {"short_date", v.ShortDate}, {"long_date", v.LongDate}, {"day_of_week", v.DayOfWeek}} {
				if flag.enabled {
					e.DateFormat = append(e.DateFormat, flag.name)
				}
			}
		default:
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		entities = append(entities, e)
	}
	if err := model.ValidateTextEntities(text, entities); err != nil {
		return nil, err
	}
	return entities, nil
}
