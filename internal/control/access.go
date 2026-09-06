package control

import (
	"context"
	"fmt"
	"io"
)

type accessController interface {
	FullRead(context.Context) (bool, error)
	SetFullRead(context.Context, bool) error
}

func runAccessCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller) int {
	inspect := len(args) == 1
	enable := len(args) == 3 && args[1] == "full" && args[2] == "--accept-full-read"
	disable := len(args) == 2 && args[1] == "restricted"
	if !inspect && !enable && !disable {
		fmt.Fprintln(stderr, "telegram-mcp: use access, access full --accept-full-read, or access restricted; see --help for disclosure and read effects")
		return 2
	}
	access, ok := control.(accessController)
	if !ok {
		fmt.Fprintln(stderr, "telegram-mcp: access control is unavailable")
		return 1
	}
	var err error
	if inspect {
		var enabled bool
		enabled, err = access.FullRead(ctx)
		if err == nil {
			mode := "restricted"
			if enabled {
				mode = "full"
			}
			err = writeTextJSON(stdout, struct {
				Mode string `json:"mode"`
			}{mode})
		}
	} else {
		err = access.SetFullRead(ctx, enable)
		if err == nil {
			if enable {
				_, err = fmt.Fprintln(stdout, "Full read access enabled: supported conversations, all supported authors and history, images, PDF/plain-text attachments, voice notes, and read acknowledgments are available to the connected agent until revoked or the account changes.")
			} else {
				_, err = fmt.Fprintln(stdout, "Full read access disabled. Existing restricted grants apply; previously delivered content and read acknowledgments cannot be recalled.")
			}
		}
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}
