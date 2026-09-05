package policy

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

var epochPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22,128}$`)

type Repository struct {
	database *sql.DB
	lockPath string
	now      func() time.Time
}

func New(database *sql.DB, lockPath string, now func() time.Time) (*Repository, error) {
	if database == nil || !filepath.IsAbs(lockPath) || now == nil {
		return nil, model.TextError(model.ErrorInvalidInput, errors.New("policy database, absolute lock path, and clock are required"))
	}
	return &Repository{database: database, lockPath: lockPath, now: now}, nil
}

// Lease serializes policy decisions and mutations across processes. It holds no
// database transaction between calls, permitting Telegram checkpoint writes.
type Lease struct {
	repository *Repository
	lock       *daemon.AccountLock
	epoch      string
	mu         sync.Mutex
	closed     bool
}

func (r *Repository) Acquire(ctx context.Context) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, model.TextError(model.ErrorCancelled, err)
	}
	lock, err := daemon.AcquireAccountLock(r.lockPath)
	if err != nil {
		if errors.Is(err, daemon.ErrAccountLocked) {
			return nil, model.TextError(model.ErrorNotReady, errors.Join(ErrBusy, err))
		}
		return nil, internalError(err)
	}
	lease := &Lease{repository: r, lock: lock}
	err = r.database.QueryRowContext(ctx, "SELECT epoch FROM authorization_state WHERE singleton = 1").Scan(&lease.epoch)
	if err == nil && !epochPattern.MatchString(lease.epoch) {
		err = ErrEpochChanged
	}
	if err != nil {
		releaseErr := lock.Release()
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrEpochChanged) {
			return nil, model.TextError(model.ErrorReauthRequired, errors.Join(ErrEpochChanged, releaseErr))
		}
		return nil, internalError(errors.Join(err, releaseErr))
	}
	return lease, nil
}

func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if err := l.lock.Release(); err != nil {
		return internalError(err)
	}
	return nil
}

func (l *Lease) transaction(ctx context.Context, operation func(*sql.Tx) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return internalError(ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return model.TextError(model.ErrorCancelled, err)
	}
	tx, err := l.repository.database.BeginTx(ctx, nil)
	if err != nil {
		return internalError(err)
	}
	defer tx.Rollback()
	var epoch string
	err = tx.QueryRowContext(ctx, "SELECT epoch FROM authorization_state WHERE singleton = 1").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && epoch != l.epoch) {
		return model.TextError(model.ErrorReauthRequired, ErrEpochChanged)
	}
	if err != nil {
		return internalError(err)
	}
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return internalError(err)
	}
	return nil
}

const grantColumns = "peer, author, min_id, max_id, read_through, profile, expires_at, eligible, images"

func scanGrant(row interface{ Scan(...any) error }) (Grant, error) {
	var grant Grant
	var peer, author, expiry string
	if err := row.Scan(&peer, &author, &grant.MinID, &grant.MaxID, &grant.ReadThrough, &grant.Profile, &expiry, &grant.Eligible, &grant.Images); err != nil {
		return Grant{}, err
	}
	var err error
	grant.Peer, err = model.ParsePeerID(peer)
	if err != nil {
		return Grant{}, err
	}
	grant.Author, err = model.ParsePeerID(author)
	if err != nil {
		return Grant{}, err
	}
	grant.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return Grant{}, err
	}
	if err := grant.validateFields(); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

func (l *Lease) Grant(ctx context.Context, peer model.PeerID) (Grant, error) {
	var grant Grant
	err := l.transaction(ctx, func(tx *sql.Tx) error {
		full, err := fullReadEnabled(ctx, tx, l.epoch)
		if err != nil {
			return err
		}
		if full {
			if peer.String() == "" {
				return model.TextError(model.ErrorInvalidReference, nil)
			}
			grant = fullReadGrant(peer)
			return nil
		}
		grant, err = scanGrant(tx.QueryRowContext(ctx, "SELECT "+grantColumns+" FROM text_grants WHERE peer = ? AND authorization_epoch = ?", peer.String(), l.epoch))
		if errors.Is(err, sql.ErrNoRows) {
			return model.TextError(model.ErrorPolicyDenied, ErrDenied)
		}
		if err != nil {
			return internalError(err)
		}
		now := l.repository.now()
		if !grant.ExpiresAt.After(now) {
			return model.TextError(model.ErrorPolicyDenied, ErrDenied)
		}
		if err := grant.Validate(now); err != nil {
			return internalError(err)
		}
		return nil
	})
	if err != nil {
		return Grant{}, err
	}
	return grant, nil
}

func (l *Lease) List(ctx context.Context) ([]Grant, error) {
	grants := make([]Grant, 0)
	err := l.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+grantColumns+" FROM text_grants WHERE authorization_epoch = ? ORDER BY peer", l.epoch)
		if err != nil {
			return internalError(err)
		}
		defer rows.Close()
		now := l.repository.now()
		for rows.Next() {
			grant, err := scanGrant(rows)
			if err != nil {
				return internalError(err)
			}
			if !grant.ExpiresAt.After(now) {
				continue
			}
			if err := grant.Validate(now); err != nil {
				return internalError(err)
			}
			grants = append(grants, grant)
			if len(grants) > MaximumGrants {
				return internalError(errors.New("text grant limit exceeded"))
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
	return grants, nil
}

func (l *Lease) Save(ctx context.Context, grant Grant) error {
	if err := grant.Validate(l.repository.now()); err != nil {
		return err
	}
	return l.transaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM text_grants WHERE peer != ?", grant.Peer.String()).Scan(&count); err != nil {
			return internalError(err)
		}
		if count >= MaximumGrants {
			return model.TextError(model.ErrorInvalidInput, errors.New("text grant limit reached; revoke an existing grant"))
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO text_grants (peer,authorization_epoch,author,min_id,max_id,read_through,profile,expires_at,eligible,images)
   VALUES (?,?,?,?,?,?,?,?,1,?)
   ON CONFLICT(peer) DO UPDATE SET authorization_epoch=excluded.authorization_epoch,
   author=excluded.author,min_id=excluded.min_id,max_id=excluded.max_id,read_through=excluded.read_through,
   profile=excluded.profile,expires_at=excluded.expires_at,eligible=excluded.eligible,images=excluded.images`,
			grant.Peer.String(), l.epoch, grant.Author.String(), grant.MinID, grant.MaxID, grant.ReadThrough, grant.Profile, grant.ExpiresAt.UTC().Format(time.RFC3339Nano), grant.Images)
		if err != nil {
			return internalError(err)
		}
		return nil
	})
}

func (l *Lease) Revoke(ctx context.Context, peer model.PeerID) error {
	if peer.String() == "" {
		return model.TextError(model.ErrorInvalidReference, model.ErrInvalidReference)
	}
	return l.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM text_grants WHERE peer = ? AND authorization_epoch = ?", peer.String(), l.epoch); err != nil {
			return internalError(err)
		}
		return nil
	})
}

func (l *Lease) Audit(ctx context.Context, requestID, operation string, category model.ErrorCategory, count int, uncertain bool) error {
	if !model.IsValidRequestID(requestID) || (operation != "list_chats" && operation != "list_messages" && operation != "get_message_context" && operation != "search_messages" && operation != "list_unread" && operation != "list_scopes" && operation != "open_image" && operation != "catch_up") ||
		(category != "" && !category.IsValid()) || count < 0 || count > model.MaximumPageSize ||
		(category != "" && count != 0) || (category == "" && uncertain) {
		return model.TextError(model.ErrorInvalidInput, errors.New("invalid text audit record"))
	}
	outcome := "success"
	if category != "" {
		outcome = "failure"
	}
	return l.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at)
   VALUES(?,?,?,?,?,?,?)`, requestID, operation, outcome, string(category), count, uncertain, l.repository.now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return internalError(err)
		}
		return nil
	})
}

func internalError(cause error) error { return model.TextError(model.ErrorInternal, cause) }

// Binding returns durable authority coordinates while the policy lease is held.
func (l *Lease) Binding(ctx context.Context) (epoch string, revision int64, err error) {
	err = l.transaction(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT revision FROM policy_revision WHERE singleton = 1").Scan(&revision); err != nil {
			return internalError(err)
		}
		if revision <= 0 {
			return internalError(errors.New("invalid policy revision"))
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	return l.epoch, revision, nil
}
