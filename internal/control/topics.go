package control

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func runTopicsCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller) int {
	values := map[string]string{}
	for i := 1; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return textUsageError(stderr)
		}
		key := args[i]
		if key != "--peer" && key != "--offset-date" && key != "--offset-message" && key != "--offset-topic" {
			return textUsageError(stderr)
		}
		if _, ok := values[key]; ok {
			return textUsageError(stderr)
		}
		values[key] = args[i+1]
	}
	peer, err := model.ParsePeerID(values["--peer"])
	if err != nil || peer.Kind() != model.PeerKindChannel || peer.TopicID() != 0 {
		return textUsageError(stderr)
	}
	position := model.TopicPosition{}
	if len(values) != 1 {
		if len(values) != 4 {
			return textUsageError(stderr)
		}
		for key, target := range map[string]*int{"--offset-date": &position.Date, "--offset-message": &position.Message, "--offset-topic": &position.Topic} {
			n, err := strconv.ParseInt(values[key], 10, 32)
			if err != nil || n <= 0 || strconv.FormatInt(n, 10) != values[key] {
				return textUsageError(stderr)
			}
			*target = int(n)
		}
	}
	c, ok := control.(interface {
		Topics(context.Context, model.PeerID, model.TopicPosition) (model.TopicPage, error)
	})
	if !ok {
		fmt.Fprintln(stderr, "telegram-mcp: topic discovery is unavailable")
		return 1
	}
	page, err := c.Topics(ctx, peer, position)
	if err == nil {
		err = writeTextJSON(stdout, struct {
			Items []model.Topic        `json:"items"`
			Next  *model.TopicPosition `json:"next_position"`
		}{page.Items, page.Next})
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}
