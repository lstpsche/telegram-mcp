package telegram

import "github.com/gotd/td/tg"

// commonUpdates excludes independent channel pts streams while retaining the
// enclosing seq/date and all common pts/qts events. Supergroup reads use live
// RPC responses and receipt readback, not channel subscriptions or cached bodies.
func commonUpdates(value tg.UpdatesClass) (tg.UpdatesClass, error) {
	switch value := value.(type) {
	case *tg.Updates:
		copy := *value
		var err error
		copy.Updates, err = commonEvents(value.Updates)
		if err != nil {
			return nil, err
		}
		copy.Chats = commonChats(value.Chats)
		return &copy, nil
	case *tg.UpdatesCombined:
		copy := *value
		var err error
		copy.Updates, err = commonEvents(value.Updates)
		if err != nil {
			return nil, err
		}
		copy.Chats = commonChats(value.Chats)
		return &copy, nil
	case *tg.UpdateShort:
		events, err := commonEvents([]tg.UpdateClass{value.Update})
		if err != nil {
			return nil, err
		}
		return &tg.Updates{Updates: events, Date: value.Date}, nil
	default:
		return value, nil
	}
}

func commonEvents(events []tg.UpdateClass) ([]tg.UpdateClass, error) {
	result := make([]tg.UpdateClass, 0, len(events))
	for _, event := range events {
		_, _, _, channel, err := tg.IsChannelPtsUpdate(event)
		if err != nil {
			return nil, err
		}
		if channel {
			continue
		}
		result = append(result, event)
	}
	return result, nil
}

func commonChats(chats []tg.ChatClass) []tg.ChatClass {
	result := make([]tg.ChatClass, 0, len(chats))
	for _, chat := range chats {
		switch chat.(type) {
		case *tg.Channel, *tg.ChannelForbidden:
			continue
		}
		result = append(result, chat)
	}
	return result
}

// Recovery validates the complete response and its budgets before projection.
// The accepted common checkpoint is retained verbatim; no channel checkpoint
// or successful channel-difference response is manufactured.
func commonDifference(value tg.UpdatesDifferenceClass) (tg.UpdatesDifferenceClass, error) {
	switch value := value.(type) {
	case *tg.UpdatesDifference:
		copy := *value
		var err error
		copy.OtherUpdates, err = commonEvents(value.OtherUpdates)
		if err != nil {
			return nil, err
		}
		copy.Chats = commonChats(value.Chats)
		return &copy, nil
	case *tg.UpdatesDifferenceSlice:
		copy := *value
		var err error
		copy.OtherUpdates, err = commonEvents(value.OtherUpdates)
		if err != nil {
			return nil, err
		}
		copy.Chats = commonChats(value.Chats)
		return &copy, nil
	default:
		return value, nil
	}
}
