package control

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func parseFolderID(value string) (int32, bool) {
	n, err := strconv.ParseInt(value, 10, 32)
	return int32(n), err == nil && n >= 2 && strconv.FormatInt(n, 10) == value
}

func runFoldersCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller) int {
	var id int32
	if len(args) != 1 {
		if len(args) != 3 || args[1] != "--id" {
			return scopeUsageError(stderr)
		}
		var ok bool
		id, ok = parseFolderID(args[2])
		if !ok {
			return scopeUsageError(stderr)
		}
	}
	discovery, ok := control.(interface {
		Folders(context.Context, int32) ([]model.Folder, error)
	})
	if !ok {
		fmt.Fprintln(stderr, "telegram-mcp: folder discovery is unavailable")
		return 1
	}
	folders, err := discovery.Folders(ctx, id)
	if err == nil {
		err = writeTextJSON(stdout, folders)
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}
