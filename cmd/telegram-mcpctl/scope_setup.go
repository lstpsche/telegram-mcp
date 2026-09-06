package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func runScopeSetupCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller, openTerminal terminalFactory) int {
	if len(args) != 2 {
		return scopeUsageError(stderr)
	}
	prompt, err := openTerminal()
	if err == nil {
		interactive, ok := prompt.(setupTerminal)
		if !ok {
			err = humanHint("interactive scope setup is unavailable")
		} else {
			s := setupSession{control: control, prompt: interactive, output: stdout}
			err = s.guideScope(ctx)
		}
		err = errors.Join(err, prompt.Close())
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}

func (s *setupSession) chooseScope(ctx context.Context) error {
	answer, err := s.prompt.Ask(ctx, "Set up a named scope for catch-up? yes or skip [Enter]: ")
	if err != nil {
		return err
	}
	switch answer {
	case "", "skip":
		return nil
	case "yes":
		return s.guideScope(ctx)
	default:
		return humanHint("choose yes or skip for scope setup")
	}
}

func (s *setupSession) guideScope(ctx context.Context) error {
	control, ok := s.control.(scopeController)
	if !ok {
		return humanHint("scope control is unavailable")
	}
	text, ok := s.control.(textController)
	if !ok {
		return humanHint("text control is unavailable")
	}
	scopes, err := control.Scopes(ctx)
	if err != nil {
		return err
	}
	grants, err := text.Grants(ctx)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(s.output, "Scopes group conversations for search and catch-up. They do not grant access or restrict Full read authority. This guide uses local metadata; it does not contact Telegram."); err != nil {
		return err
	}
	if len(scopes) > 0 {
		if err := writeTextJSON(s.output, scopes); err != nil {
			return err
		}
	}
	name, err := s.prompt.Ask(ctx, "Scope name (1–32 lowercase letters, digits, _ or -, starting with a letter; use an existing name to replace its members): ")
	if err != nil {
		return err
	}
	if !model.ValidScopeName(name) {
		return humanHint("invalid scope name; use 1–32 lowercase letters, digits, _ or -, starting with a letter")
	}
	var id model.ScopeID
	choices := make([]model.PeerID, 0)
	for _, scope := range scopes {
		if scope.Name == name {
			id = scope.ID
			choices = append(choices, scope.Peers...)
		}
	}
	if id == "" && len(scopes) >= policy.MaximumScopes {
		return humanHint("scope limit reached; replace an existing scope or remove one with unscope")
	}
	for _, grant := range grants {
		if !slices.Contains(choices, grant.Peer) {
			choices = append(choices, grant.Peer)
		}
	}
	if _, err := fmt.Fprintln(s.output, "Choices come from this scope's current members and stored grants. Listing a peer does not establish current access; grants may expire. You can also paste exact peer IDs from authorized chat discovery."); err != nil {
		return err
	}
	for index, peer := range choices {
		if err := writeTextJSON(s.output, struct {
			Number int          `json:"number"`
			Peer   model.PeerID `json:"peer"`
		}{index + 1, peer}); err != nil {
			return err
		}
	}
	answer, err := s.prompt.Ask(ctx, "Complete membership: space-separated choice numbers or tgpeer:v1 IDs (maximum 20); type empty for no members: ")
	if err != nil {
		return err
	}
	peers, err := scopeSelection(answer, choices)
	if err != nil {
		return err
	}
	if err := writeTextJSON(s.output, struct {
		Name  string         `json:"name"`
		Peers []model.PeerID `json:"peers"`
	}{name, peers}); err != nil {
		return err
	}
	if err := s.confirm(ctx, "Save this complete scope membership, replacing any previous members under this name?"); err != nil {
		return err
	}
	scope, err := control.Scope(ctx, id, name, peers)
	if err != nil {
		return err
	}
	if err := writeTextJSON(s.output, scope); err != nil {
		return err
	}
	_, err = fmt.Fprintln(s.output, "Scope saved. Ask your agent to list Telegram scopes, then catch up on this scope for an explicit time window. Empty or expired access can produce no eligible messages; use access setup to review restricted grants.")
	return err
}

func scopeSelection(answer string, choices []model.PeerID) ([]model.PeerID, error) {
	peers := make([]model.PeerID, 0)
	if answer == "empty" {
		return peers, nil
	}
	values := strings.Fields(answer)
	if len(values) == 0 || len(values) > policy.MaximumScopePeers {
		return nil, humanHint("select 1–20 unique peers, or type empty explicitly")
	}
	seen := make(map[model.PeerID]bool, len(values))
	for _, value := range values {
		var peer model.PeerID
		if number, err := strconv.Atoi(value); err == nil && strconv.Itoa(number) == value && number > 0 && number <= len(choices) {
			peer = choices[number-1]
		} else {
			var err error
			peer, err = model.ParsePeerID(value)
			if err != nil {
				return nil, humanHint("invalid member; use a displayed choice number or an exact tgpeer:v1 ID")
			}
		}
		if seen[peer] {
			return nil, humanHint("duplicate peer selected; include each member only once")
		}
		seen[peer] = true
		peers = append(peers, peer)
	}
	return peers, nil
}
