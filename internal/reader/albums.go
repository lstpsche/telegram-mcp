package reader

import (
	"sort"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

// contextFetchQuery reserves the entire candidate window before any I/O.
func contextFetchQuery(query model.HistoryQuery) model.HistoryQuery {
	if query.ExpandAlbum {
		query.BeforeCount = max(query.BeforeCount, 9)
		query.AfterCount = max(query.AfterCount, 9)
		query.Limit = 1 + query.BeforeCount + query.AfterCount
	}
	return query
}

// selectAlbumContext narrows an already validated and authorized scan. Neighbor
// positions count all candidates, including withheld messages, as ordinary context does.
func selectAlbumContext(query model.HistoryQuery, candidates []model.Candidate, items []model.Message) []model.Message {
	older, newer := []int32{}, []int32{}
	for _, candidate := range candidates {
		id := candidate.Message.ID.TelegramID()
		if id < query.Target {
			older = append(older, id)
		}
		if id > query.Target {
			newer = append(newer, id)
		}
	}
	sort.Slice(older, func(i, j int) bool { return older[i] > older[j] })
	sort.Slice(newer, func(i, j int) bool { return newer[i] < newer[j] })
	selected := map[int32]bool{query.Target: true}
	for _, id := range older[:min(len(older), query.BeforeCount)] {
		selected[id] = true
	}
	for _, id := range newer[:min(len(newer), query.AfterCount)] {
		selected[id] = true
	}
	album := ""
	for _, item := range items {
		if item.ID.TelegramID() == query.Target {
			album = item.AlbumID
		}
	}
	context := &model.AlbumContext{State: "not_album", Messages: []model.MessageID{}}
	if album != "" {
		context.State = "bounded"
	}
	out := make([]model.Message, 0, len(items))
	for _, item := range items {
		member := album != "" && item.AlbumID == album
		if !member && !selected[item.ID.TelegramID()] {
			continue
		}
		if member {
			context.Messages = append(context.Messages, item.ID)
		}
		if item.ID.TelegramID() == query.Target {
			item.AlbumContext = context
		}
		out = append(out, item)
	}
	sort.Slice(context.Messages, func(i, j int) bool { return context.Messages[i].TelegramID() > context.Messages[j].TelegramID() })
	return out
}
