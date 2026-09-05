package policy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

const (
	MaximumScopes     = 20
	MaximumScopePeers = 20
)

var ErrInvalidScope = errors.New("invalid named scope")

// Scope is a local selection of peers, independent of their current grants.
type Scope struct {
	ID    model.ScopeID  `json:"id"`
	Name  string         `json:"name"`
	Peers []model.PeerID `json:"peers"`
}

func validateScope(name string, peers []model.PeerID) error {
	if !model.ValidScopeName(name) || len(peers) > MaximumScopePeers {
		return model.TextError(model.ErrorInvalidInput, ErrInvalidScope)
	}
	seen := make(map[model.PeerID]struct{}, len(peers))
	for _, peer := range peers {
		if peer.String() == "" {
			return model.TextError(model.ErrorInvalidInput, ErrInvalidScope)
		}
		if _, exists := seen[peer]; exists {
			return model.TextError(model.ErrorInvalidInput, ErrInvalidScope)
		}
		seen[peer] = struct{}{}
	}
	return nil
}

func (l *Lease) SaveScope(ctx context.Context, id model.ScopeID, name string, peers []model.PeerID) (Scope, error) {
	if id != "" && id.String() == "" {
		return Scope{}, model.TextError(model.ErrorInvalidReference, model.ErrInvalidReference)
	}
	if err := validateScope(name, peers); err != nil {
		return Scope{}, err
	}
	scope := Scope{ID: id, Name: name, Peers: append(make([]model.PeerID, 0, len(peers)), peers...)}
	sort.Slice(scope.Peers, func(i, j int) bool { return scope.Peers[i].String() < scope.Peers[j].String() })
	err := l.transaction(ctx, func(tx *sql.Tx) error {
		var nameID string
		err := tx.QueryRowContext(ctx, "SELECT id FROM named_scopes WHERE name = ? AND authorization_epoch = ?", name, l.epoch).Scan(&nameID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return internalError(err)
		}
		if id != "" {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM named_scopes WHERE id = ? AND authorization_epoch = ?", id.String(), l.epoch).Scan(&exists); err != nil {
				return internalError(err)
			}
			if exists != 1 {
				return model.TextError(model.ErrorInvalidReference, model.ErrInvalidReference)
			}
			if nameID != "" && nameID != id.String() {
				return model.TextError(model.ErrorInvalidInput, ErrInvalidScope)
			}
		} else if nameID != "" {
			var err error
			scope.ID, err = model.ParseScopeID(nameID)
			if err != nil {
				return internalError(err)
			}
		}
		if scope.ID == "" {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM named_scopes").Scan(&count); err != nil {
				return internalError(err)
			}
			if count >= MaximumScopes {
				return model.TextError(model.ErrorInvalidInput, ErrInvalidScope)
			}
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return internalError(err)
			}
			scope.ID = model.ScopeID("tgscope:v1:" + hex.EncodeToString(random[:]))
			if _, err := tx.ExecContext(ctx, "INSERT INTO named_scopes(id,name,authorization_epoch) VALUES(?,?,?)", scope.ID.String(), scope.Name, l.epoch); err != nil {
				return internalError(err)
			}
		} else {
			if _, err := tx.ExecContext(ctx, "UPDATE named_scopes SET name = ? WHERE id = ?", scope.Name, scope.ID.String()); err != nil {
				return internalError(err)
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM named_scope_peers WHERE scope_id = ?", scope.ID.String()); err != nil {
			return internalError(err)
		}
		for _, peer := range scope.Peers {
			if _, err := tx.ExecContext(ctx, "INSERT INTO named_scope_peers(scope_id,peer) VALUES(?,?)", scope.ID.String(), peer.String()); err != nil {
				return internalError(err)
			}
		}
		return nil
	})
	if err != nil {
		return Scope{}, err
	}
	return scope, nil
}

func (l *Lease) Scopes(ctx context.Context) ([]Scope, error) {
	scopes := make([]Scope, 0)
	err := l.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT s.id,s.name,p.peer FROM named_scopes s
		LEFT JOIN named_scope_peers p ON p.scope_id = s.id
		WHERE s.authorization_epoch = ? ORDER BY s.id,p.peer`, l.epoch)
		if err != nil {
			return internalError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var rawID, name string
			var rawPeer sql.NullString
			if err := rows.Scan(&rawID, &name, &rawPeer); err != nil {
				return internalError(err)
			}
			id, err := model.ParseScopeID(rawID)
			if err != nil {
				return internalError(err)
			}
			if len(scopes) == 0 || scopes[len(scopes)-1].ID != id {
				scopes = append(scopes, Scope{ID: id, Name: name, Peers: make([]model.PeerID, 0)})
				if len(scopes) > MaximumScopes {
					return internalError(ErrInvalidScope)
				}
			}
			scope := &scopes[len(scopes)-1]
			if rawPeer.Valid {
				peer, err := model.ParsePeerID(rawPeer.String)
				if err != nil {
					return internalError(err)
				}
				scope.Peers = append(scope.Peers, peer)
			}
			if err := validateScope(scope.Name, scope.Peers); err != nil {
				return internalError(ErrInvalidScope)
			}
		}
		if err := rows.Err(); err != nil {
			return internalError(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scopes, nil
}

func (l *Lease) Scope(ctx context.Context, id model.ScopeID) (Scope, error) {
	if id.String() == "" {
		return Scope{}, model.TextError(model.ErrorInvalidReference, model.ErrInvalidReference)
	}
	scopes, err := l.Scopes(ctx)
	if err != nil {
		return Scope{}, err
	}
	for _, scope := range scopes {
		if scope.ID == id {
			return scope, nil
		}
	}
	return Scope{}, model.TextError(model.ErrorInvalidReference, model.ErrInvalidReference)
}

func (l *Lease) DeleteScope(ctx context.Context, id model.ScopeID) error {
	if id.String() == "" {
		return model.TextError(model.ErrorInvalidReference, model.ErrInvalidReference)
	}
	return l.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM named_scopes WHERE id = ? AND authorization_epoch = ?", id.String(), l.epoch)
		if err != nil {
			return internalError(err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return internalError(err)
		}
		if count != 1 {
			return model.TextError(model.ErrorInvalidReference, model.ErrInvalidReference)
		}
		return nil
	})
}
