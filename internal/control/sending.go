package control

import (
	"context"
	"fmt"
	"io"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

type sendingController interface {
	FullSend(context.Context) (bool, error)
	SetFullSend(context.Context, bool) error
}

type sendStatusController interface {
	SendOperationStatus(context.Context, string) (model.SendOperation, error)
}

func runSendingCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller) int {
	if len(args) > 1 && args[1] == "status" {
		return runSendStatusCommand(ctx, args, stdout, stderr, control)
	}
	inspect := len(args) == 1
	enable := len(args) == 3 && args[1] == "enable" && args[2] == "--accept-account-wide-send"
	disable := len(args) == 2 && args[1] == "disable"
	if !inspect && !enable && !disable {
		fmt.Fprintln(stderr, "telegram-mcp: use sending, sending enable --accept-account-wide-send, sending disable, or sending status --operation-id UUID; see --help for authority and routing-metadata disclosure")
		return 2
	}
	sending, ok := control.(sendingController)
	if !ok {
		fmt.Fprintln(stderr, "telegram-mcp: sending control is unavailable")
		return 1
	}
	var err error
	if inspect {
		var enabled bool
		enabled, err = sending.FullSend(ctx)
		if err == nil {
			err = writeTextJSON(stdout, struct {
				Enabled bool `json:"enabled"`
			}{enabled})
		}
	} else {
		err = sending.SetFullSend(ctx, enable)
		if err == nil {
			if enable {
				_, err = fmt.Fprintln(stdout, "Account-wide sending enabled. "+sendingDisclosure)
			} else {
				_, err = fmt.Fprintln(stdout, "Sending disabled. Read access is unchanged; retained send outcomes remain available through sending status.")
			}
		}
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}

func runSendStatusCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller) int {
	if len(args) != 4 || args[2] != "--operation-id" {
		fmt.Fprintln(stderr, "telegram-mcp: sending status requires --operation-id UUID")
		return 2
	}
	if _, err := model.ParseOperationID(args[3]); err != nil {
		fmt.Fprintln(stderr, "telegram-mcp: sending status requires a canonical UUIDv7 operation ID")
		return 2
	}
	status, ok := control.(sendStatusController)
	if !ok {
		fmt.Fprintln(stderr, "telegram-mcp: retained send status is unavailable")
		return 1
	}
	operation, err := status.SendOperationStatus(ctx, args[3])
	if err == nil {
		err = writeTextJSON(stdout, operation)
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}

const sendingDisclosure = "Account-wide sending lets connected agents resolve recipients and send as this account to all supported recipients, including new conversations and supported channel posts, without per-recipient confirmation. Sending to bots can trigger bot behavior. This permits bounded recipient and reply-routing metadata lookups; incidental message bodies are discarded inside the adapter and are never exposed or persisted. Sending grants no message-body access and does not change read access or read acknowledgments. Read scopes do not restrict sending. This setting persists across ordinary restarts until revoked, logout, account change, or settings restore."
