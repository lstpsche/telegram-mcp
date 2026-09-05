package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type textController interface {
	Peers(context.Context) ([]model.Chat, error)
	Grants(context.Context) ([]policy.Grant, error)
	Grant(context.Context, policy.Grant) error
	Revoke(context.Context, model.PeerID) error
}

func runTextCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller) int {
	var grant policy.Grant
	var peer model.PeerID
	var err error
	switch args[0] {
	case "peers", "grants":
		if len(args) != 1 {
			return textUsageError(stderr)
		}
	case "grant":
		var ok bool
		grant, ok = parseGrant(args[1:])
		if !ok {
			return textUsageError(stderr)
		}
	case "revoke":
		if len(args) != 3 || args[1] != "--peer" {
			return textUsageError(stderr)
		}
		peer, err = model.ParsePeerID(args[2])
		if err != nil {
			return textUsageError(stderr)
		}
	}
	textControl, ok := control.(textController)
	if !ok {
		fmt.Fprintln(stderr, "telegram-mcpctl: text access control is unavailable")
		return 1
	}
	switch args[0] {
	case "peers":
		var peers []model.Chat
		peers, err = textControl.Peers(ctx)
		if err == nil {
			if peers == nil {
				peers = []model.Chat{}
			}
			err = writeTextJSON(stdout, peers)
		}
	case "grants":
		var grants []policy.Grant
		grants, err = textControl.Grants(ctx)
		if err == nil {
			records := make([]grantRecord, 0, len(grants))
			for _, item := range grants {
				records = append(records, newGrantRecord(item))
			}
			err = writeTextJSON(stdout, records)
		}
	case "grant":
		err = textControl.Grant(ctx, grant)
		if err == nil {
			_, err = fmt.Fprintln(stdout, "Text grant saved for the current authorization epoch.")
		}
	case "revoke":
		err = textControl.Revoke(ctx, peer)
		if err == nil {
			_, err = fmt.Fprintln(stdout, "Text grant revoked.")
		}
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}

func textUsageError(stderr io.Writer) int {
	fmt.Fprintln(stderr, "telegram-mcpctl: invalid text access arguments; see --help")
	return 2
}

// Parse every field exactly once. No submitted value is included in diagnostics.
func parseGrant(args []string) (policy.Grant, bool) {
	var grant policy.Grant
	values := make(map[string]string, 7)
	attested := false
	for index := 0; index < len(args); index++ {
		key := args[index]
		if key == "--attest-eligible" {
			if attested {
				return grant, false
			}
			attested = true
			continue
		}
		if key == "--allow-images" {
			if grant.Images {
				return grant, false
			}
			grant.Images = true
			continue
		}
		switch key {
		case "--peer", "--author", "--min-id", "--max-id", "--read-through", "--expires-at", "--profile":
		default:
			return grant, false
		}
		if _, exists := values[key]; exists || index+1 >= len(args) {
			return grant, false
		}
		index++
		values[key] = args[index]
	}
	if !attested || len(values) != 7 {
		return grant, false
	}
	var err error
	grant.Peer, err = model.ParsePeerID(values["--peer"])
	if err != nil {
		return grant, false
	}
	grant.Author, err = model.ParsePeerID(values["--author"])
	if err != nil || grant.Author.Kind() != model.PeerKindUser {
		return grant, false
	}
	grant.MinID, err = parseMessageNumber(values["--min-id"])
	if err != nil {
		return grant, false
	}
	grant.MaxID, err = parseMessageNumber(values["--max-id"])
	if err != nil || grant.MaxID < grant.MinID {
		return grant, false
	}
	grant.ReadThrough, err = parseReadCeiling(values["--read-through"])
	if err != nil {
		return grant, false
	}
	grant.ExpiresAt, err = time.Parse(time.RFC3339, values["--expires-at"])
	if err != nil {
		return grant, false
	}
	switch values["--profile"] {
	case policy.ProfileSelfAuthored, policy.ProfileConsented:
		grant.Profile = values["--profile"]
	default:
		return grant, false
	}
	grant.Eligible = true
	return grant, true
}

func parseMessageNumber(value string) (int32, error) {
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < 1 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("invalid message identifier")
	}
	return int32(parsed), nil
}

func parseReadCeiling(value string) (int32, error) {
	if value == "0" {
		return 0, nil
	}
	return parseMessageNumber(value)
}

type grantRecord struct {
	Peer        model.PeerID `json:"peer"`
	Author      model.PeerID `json:"author"`
	MinID       int32        `json:"min_id,string"`
	MaxID       int32        `json:"max_id,string"`
	ReadThrough int32        `json:"read_through,string"`
	Profile     string       `json:"profile"`
	ExpiresAt   time.Time    `json:"expires_at"`
	Eligible    bool         `json:"eligible"`
	Images      bool         `json:"images"`
}

func newGrantRecord(grant policy.Grant) grantRecord {
	return grantRecord{Peer: grant.Peer, Author: grant.Author, MinID: grant.MinID, MaxID: grant.MaxID,
		ReadThrough: grant.ReadThrough, Profile: grant.Profile, ExpiresAt: grant.ExpiresAt, Eligible: grant.Eligible, Images: grant.Images}
}

// Encode the whole result before writing. JSON quoting prevents terminal control
// sequences from Telegram display names being interpreted by the terminal.
func writeTextJSON(writer io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// JSON leaves DEL and C1 controls literal; escape them for terminal output.
	var safe strings.Builder
	for _, char := range string(encoded) {
		if char >= 0x7f && char <= 0x9f {
			fmt.Fprintf(&safe, "\\u%04x", char)
		} else {
			safe.WriteRune(char)
		}
	}
	safe.WriteByte('\n')
	_, err = io.WriteString(writer, safe.String())
	return err
}
