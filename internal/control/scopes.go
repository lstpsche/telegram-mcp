package control

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
	var folder int32
	var err error
	switch args[0] {
	case "scopes":
		if len(args) != 1 {
			return scopeUsageError(stderr)
		}
	case "scope":
		var ok bool
		id, name, peers, folder, ok = parseScope(args[1:])
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
		fmt.Fprintln(stderr, "telegram-mcp: scope control is unavailable")
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
		if folder == 0 {
			scope, err = scopeControl.Scope(ctx, id, name, peers)
		} else {
			importer, ok := control.(interface {
				ScopeFromFolder(context.Context, model.ScopeID, string, int32) (policy.Scope, error)
			})
			if !ok {
				fmt.Fprintln(stderr, "telegram-mcp: folder import is unavailable")
				return 1
			}
			scope, err = importer.ScopeFromFolder(ctx, id, name, folder)
		}
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
	fmt.Fprintln(stderr, "telegram-mcp: invalid named scope arguments; see --help")
	return 2
}

func parseScope(args []string) (model.ScopeID, string, []model.PeerID, int32, bool) {
	var id model.ScopeID
	var name string
	var folder int32
	peers := make([]model.PeerID, 0)
	seenOptions := make(map[string]bool, 2)
	seenPeers := make(map[model.PeerID]bool)
	for index := 0; index < len(args); index++ {
		key := args[index]
		if index+1 >= len(args) {
			return "", "", nil, 0, false
		}
		index++
		value := args[index]
		switch key {
		case "--name":
			if seenOptions[key] || !model.ValidScopeName(value) {
				return "", "", nil, 0, false
			}
			name = value
		case "--id":
			var err error
			id, err = model.ParseScopeID(value)
			if seenOptions[key] || err != nil {
				return "", "", nil, 0, false
			}
		case "--folder":
			var ok bool
			folder, ok = parseFolderID(value)
			if seenOptions[key] || !ok {
				return "", "", nil, 0, false
			}
		case "--peer":
			peer, err := model.ParsePeerID(value)
			if err != nil || seenPeers[peer] || len(peers) >= policy.MaximumScopePeers {
				return "", "", nil, 0, false
			}
			seenPeers[peer] = true
			peers = append(peers, peer)
		default:
			return "", "", nil, 0, false
		}
		seenOptions[key] = true
	}
	return id, name, peers, folder, seenOptions["--name"] && (folder == 0 || len(peers) == 0)
}
