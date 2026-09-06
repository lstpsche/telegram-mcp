package control

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func runAccessSetupCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "telegram-mcp: access setup accepts no additional arguments")
		return 2
	}
	if err := setupDefault(ctx, stdout, true); err != nil {
		fmt.Fprintln(stderr, "telegram-mcp: access setup stopped; completed changes remain. Check access and doctor before restarting the service.")
		writeControlError(stderr, err)
		return 1
	}
	return 0
}

func (s *setupSession) runAccessSetup(ctx context.Context) error {
	status, err := s.control.Status(ctx)
	if err != nil {
		return err
	}
	if !status.Configured || !status.Authorized {
		return tgaccount.ErrReauthenticationRequired
	}
	report, err := s.local.inspect(ctx)
	if err != nil {
		return err
	}
	restart := report.MCP || status.Daemon == daemon.SocketLive
	if restart {
		installed, err := s.local.service.Inspect(ctx)
		if err != nil {
			return err
		}
		if installed == nil {
			return humanHint("stop the foreground daemon before access setup")
		}
		if err := s.confirm(ctx, "Stop the service while choosing conversation metadata? Connected clients will need to reconnect."); err != nil {
			return err
		}
		if err := s.local.service.Stop(ctx); err != nil {
			return err
		}
	}
	access, ok := s.control.(accessController)
	if !ok {
		return humanHint("access control unavailable")
	}
	full, err := access.FullRead(ctx)
	if err != nil {
		return err
	}
	if full {
		if err := s.confirm(ctx, "Full read is enabled. Disable it and configure a restricted grant? Existing restricted grants will apply immediately."); err != nil {
			return err
		}
		if err := access.SetFullRead(ctx, false); err != nil {
			return err
		}
	}
	if err := s.guideGrant(ctx, time.Now().UTC()); err != nil {
		return err
	}
	if restart {
		if err := s.local.service.Start(ctx); err != nil {
			return err
		}
		if _, err := waitForReady(ctx, s.local.inspect); err != nil {
			return err
		}
	}
	return nil
}

func (s *setupSession) guideGrant(ctx context.Context, now time.Time) error {
	control, ok := s.control.(textController)
	if !ok {
		return humanHint("text controls unavailable")
	}
	choice, err := s.prompt.Ask(ctx, "Grant: newest Saved Message (saved), choose a conversation (chats), or enter exact IDs (id): ")
	if err != nil {
		return err
	}
	var grant policy.Grant
	switch choice {
	case "saved":
		if err := s.confirm(ctx, "Discover the newest Saved Message reference without returning content or marking it read? Confirm eligibility for this metadata access."); err != nil {
			return err
		}
		message, err := control.SavedMessage(ctx)
		if err != nil {
			return err
		}
		if message.Peer().Kind() != model.PeerKindSelf {
			return model.ErrInvalidReference
		}
		grant.Peer = message.Peer()
		grant.MinID = message.TelegramID()
		grant.MaxID = grant.MinID
		grant.Author, err = model.NewPeerID(model.PeerKindUser, grant.Peer.TelegramID())
		if err != nil {
			return err
		}
		grant.Profile = policy.ProfileSelfAuthored
	case "chats", "id":
		var peers []model.Chat
		if choice == "chats" {
			if err := s.confirm(ctx, "Discover supported conversation names and IDs without returning content or marking chats read? Confirm eligibility for this metadata access."); err != nil {
				return err
			}
			peers, err = control.Peers(ctx)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(s.output, "This list covers at most the first 100 main-list dialogs. Names are untrusted; authority uses the exact ID."); err != nil {
				return err
			}
			for n, peer := range peers {
				if err := writeTextJSON(s.output, struct {
					Number int        `json:"number"`
					Chat   model.Chat `json:"chat"`
				}{n + 1, peer}); err != nil {
					return err
				}
			}
		}
		value, err := s.prompt.Ask(ctx, "Conversation number from this list, or exact tgpeer:v1:... ID: ")
		if err != nil {
			return err
		}
		grant.Peer, err = choosePeer(value, peers)
		if err != nil {
			return err
		}
		value, err = s.prompt.Ask(ctx, "Author: exact tgpeer:v1:user:... ID (one author per grant): ")
		if err != nil {
			return err
		}
		grant.Author, err = model.ParsePeerID(value)
		if err != nil || grant.Author.Kind() != model.PeerKindUser {
			return model.ErrInvalidReference
		}
		grant.Profile, err = s.prompt.Ask(ctx, "Basis: self-authored or consented. Choose only a basis you have independently established: ")
		if err != nil {
			return err
		}
		value, err = s.prompt.Ask(ctx, "First permitted message: positive number or exact tgmsg:v1:... reference: ")
		if err != nil {
			return err
		}
		grant.MinID, err = grantMessageNumber(value, grant.Peer)
		if err != nil {
			return err
		}
		value, err = s.prompt.Ask(ctx, "Last permitted message: positive number or exact tgmsg:v1:... reference: ")
		if err != nil {
			return err
		}
		grant.MaxID, err = grantMessageNumber(value, grant.Peer)
		if err != nil {
			return err
		}
	default:
		return humanHint("choose saved, chats or id")
	}
	hours, err := s.prompt.Ask(ctx, "Grant lifetime in hours, 1 to 720 [24]: ")
	if err != nil {
		return err
	}
	if hours == "" {
		hours = "24"
	}
	lifetime, err := strconv.Atoi(hours)
	if err != nil || lifetime < 1 || lifetime > 720 || strconv.Itoa(lifetime) != hours {
		return policy.ErrInvalidGrant
	}
	grant.ExpiresAt = now.Truncate(time.Second).Add(time.Duration(lifetime) * time.Hour)
	if _, err := fmt.Fprintln(s.output, "Search needs no receipt. Opening history, context, images, documents or voice notes can mark every earlier message in the conversation read, including messages outside this grant."); err != nil {
		return err
	}
	ceiling, err := s.prompt.Ask(ctx, "Permit that read effect through message number (0 or Enter = search only): ")
	if err != nil {
		return err
	}
	if ceiling == "" {
		ceiling = "0"
	}
	grant.ReadThrough, err = parseReadCeiling(ceiling)
	if err != nil {
		return err
	}
	images, err := s.prompt.Ask(ctx, "Allow supported images? yes or no [no]: ")
	if err != nil {
		return err
	}
	switch images {
	case "yes":
		grant.Images = true
	case "", "no":
	default:
		return humanHint("choose yes or no for images")
	}
	documents, err := s.prompt.Ask(ctx, "Allow PDF and plain-text attachments? yes or no [no]: ")
	if err != nil {
		return err
	}
	switch documents {
	case "yes":
		grant.Documents = true
	case "", "no":
	default:
		return humanHint("choose yes or no for documents")
	}
	voice, err := s.prompt.Ask(ctx, "Allow original voice-note audio? yes or no [no]: ")
	if err != nil {
		return err
	}
	switch voice {
	case "yes":
		grant.VoiceNotes = true
	case "", "no":
	default:
		return humanHint("choose yes or no for voice notes")
	}
	grant.Eligible = true
	if err := grant.Validate(now); err != nil {
		return err
	}
	if (grant.Images || grant.Documents || grant.VoiceNotes) && grant.ReadThrough == 0 {
		if _, err := fmt.Fprintln(s.output, "Attachments may be discovered, but opening them remains denied without read-effect permission."); err != nil {
			return err
		}
	}
	if err := writeTextJSON(s.output, newGrantRecord(grant)); err != nil {
		return err
	}
	existing, err := control.Grants(ctx)
	if err != nil {
		return err
	}
	for _, old := range existing {
		if old.Peer == grant.Peer {
			if err := writeTextJSON(s.output, struct {
				Replaces grantRecord `json:"replaces"`
			}{newGrantRecord(old)}); err != nil {
				return err
			}
			if err := s.confirm(ctx, "Replace the existing grant for this conversation?"); err != nil {
				return err
			}
			break
		}
	}
	if err := s.confirm(ctx, "Save exactly this grant? Confirm eligibility for disclosure to the connected agent and model provider, the stated author/profile, and any permitted whole-prefix read effects. This attestation does not establish an exemption under Telegram terms."); err != nil {
		return err
	}
	if err := control.Grant(ctx, grant); err != nil {
		return err
	}
	_, err = fmt.Fprintln(s.output, "Restricted grant saved. Other existing grants still apply. Named scopes organize searches; they do not add or remove authority.")
	return err
}

func choosePeer(value string, peers []model.Chat) (model.PeerID, error) {
	number, err := strconv.Atoi(value)
	if err == nil && strconv.Itoa(number) == value && number >= 1 && number <= len(peers) {
		return peers[number-1].ID, nil
	}
	return model.ParsePeerID(value)
}
func grantMessageNumber(value string, peer model.PeerID) (int32, error) {
	if !strings.HasPrefix(value, "tgmsg:") {
		return parseMessageNumber(value)
	}
	message, err := model.ParseMessageID(value)
	if err != nil || message.Peer() != peer {
		return 0, model.ErrInvalidReference
	}
	return message.TelegramID(), nil
}
