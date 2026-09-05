package main

import (
	"context"
	"fmt"
	"io"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type scopeController interface {
	Scopes(context.Context) ([]policy.Scope, error)
	Scope(context.Context, model.ScopeID, string, []model.PeerID) (policy.Scope, error)
	Unscope(context.Context, model.ScopeID) error
}

func runScopeCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller) int {
	var id model.ScopeID
	var name string
	var peers []model.PeerID
	var err error
	switch args[0] {
	case "scopes":
		if len(args) != 1 {
			return scopeUsageError(stderr)
		}
	case "scope":
		var ok bool
		id, name, peers, ok = parseScope(args[1:])
		if !ok {
			return scopeUsageError(stderr)
		}
	case "unscope":
		if len(args) != 3 || args[1] != "--id" {
			return scopeUsageError(stderr)
		}
		id, err = model.ParseScopeID(args[2])
		if err != nil {
			return scopeUsageError(stderr)
		}
	default:
		return scopeUsageError(stderr)
	}
	scopeControl, ok := control.(scopeController)
	if !ok {
		fmt.Fprintln(stderr, "telegram-mcpctl: scope control is unavailable")
		return 1
	}
	switch args[0] {
	case "scopes":
		var scopes []policy.Scope
		scopes, err = scopeControl.Scopes(ctx)
		if err == nil {
			if scopes == nil {
				scopes = []policy.Scope{}
			}
			err = writeTextJSON(stdout, scopes)
		}
	case "scope":
		var scope policy.Scope
		scope, err = scopeControl.Scope(ctx, id, name, peers)
		if err == nil {
			err = writeTextJSON(stdout, scope)
		}
	case "unscope":
		err = scopeControl.Unscope(ctx, id)
		if err == nil {
			_, err = fmt.Fprintln(stdout, "Named scope removed.")
		}
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}

func scopeUsageError(stderr io.Writer) int {
	fmt.Fprintln(stderr, "telegram-mcpctl: invalid named scope arguments; see --help")
	return 2
}

func parseScope(args []string) (model.ScopeID, string, []model.PeerID, bool) {
	var id model.ScopeID
	var name string
	peers := make([]model.PeerID, 0)
	seenOptions := make(map[string]bool, 2)
	seenPeers := make(map[model.PeerID]bool)
	for index := 0; index < len(args); index++ {
		key := args[index]
		if index+1 >= len(args) {
			return "", "", nil, false
		}
		index++
		value := args[index]
		switch key {
		case "--name":
			if seenOptions[key] || !model.ValidScopeName(value) {
				return "", "", nil, false
			}
			name = value
		case "--id":
			var err error
			id, err = model.ParseScopeID(value)
			if seenOptions[key] || err != nil {
				return "", "", nil, false
			}
		case "--peer":
			peer, err := model.ParsePeerID(value)
			if err != nil || seenPeers[peer] || len(peers) >= policy.MaximumScopePeers {
				return "", "", nil, false
			}
			seenPeers[peer] = true
			peers = append(peers, peer)
		default:
			return "", "", nil, false
		}
		seenOptions[key] = true
	}
	return id, name, peers, seenOptions["--name"]
}
