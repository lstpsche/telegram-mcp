package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

const (
	OutgoingRetryWindow = model.SendDispatchWindow
	OutgoingRetention   = model.SendRetention
	outgoingCapacity    = 100000
	outgoingPruneLimit  = 1000
	phoneLookupInterval = 3 * time.Second
	phoneLookupSlot     = "contacts.resolvePhone.start"
	metadataTimeLayout  = "2006-01-02T15:04:05.000000000Z"
)

var (
	ErrInvalidOutgoingState = errors.New("outgoing metadata is invalid")
	ErrOutgoingConflict     = errors.New("outgoing operation conflicts with retained metadata")
	ErrOutgoingExpired      = errors.New("outgoing operation dispatch window has expired")
	ErrOutgoingUnavailable  = errors.New("outgoing operation is unavailable")
	ErrOutgoingCapacity     = errors.New("outgoing metadata capacity is exhausted")
	ErrOutgoingWait         = errors.New("outgoing operation is waiting for its retry time")
)

// OutgoingOperation contains only the metadata needed to prevent duplicate sends.
// PayloadDigest is keyed by the application; message text and recipient aliases
// never reach this repository.
type OutgoingOperation struct {
	OperationID        string
	AuthorizationEpoch string
	Peer               model.PeerID
	ReplyTo            *model.MessageID
	PayloadDigest      [32]byte
	RandomID           int64
	State              model.SendState
	MessageID          *model.MessageID
	SentAt             *time.Time
	FailureCode        model.SendFailureCode
	RetryAt            *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	RetainUntil        time.Time
}

func (o OutgoingOperation) Public() model.SendOperation {
	result := model.SendOperation{OperationID: o.OperationID, Peer: o.Peer, State: o.State, MessageID: o.MessageID, FailureCode: o.FailureCode}
	if o.SentAt != nil {
		result.SentAt = o.SentAt.UTC().Format(time.RFC3339Nano)
	}
	if o.RetryAt != nil {
		result.RetryAt = o.RetryAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}

type OutgoingRepository struct {
	database   *sql.DB
	capacity   int
	pruneLimit int
}

func NewOutgoingRepository(database *sql.DB) (*OutgoingRepository, error) {
	if database == nil {
		return nil, errors.New("outgoing repository database is required")
	}
	return &OutgoingRepository{database: database, capacity: outgoingCapacity, pruneLimit: outgoingPruneLimit}, nil
}

// SQLite's busy handler can outlive context cancellation. Pin the connection
// while bounding its lock wait by the invocation's remaining budget, then
// restore the ordinary timeout before returning it to the one-connection pool.
func (r *OutgoingRepository) beginWrite(ctx context.Context) (*sql.Tx, func() error, error) {
	connection, err := r.database.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	release := func() error {
		_, restoreErr := connection.ExecContext(context.Background(), fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMilliseconds))
		if restoreErr != nil {
			restoreErr = fmt.Errorf("restore outgoing SQLite lock budget: %w", restoreErr)
		}
		return errors.Join(restoreErr, connection.Close())
	}
	milliseconds := int64(busyTimeoutMilliseconds)
	if deadline, present := ctx.Deadline(); present {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil, errors.Join(context.DeadlineExceeded, release())
		}
		if budget := remaining.Milliseconds(); budget < milliseconds {
			milliseconds = budget
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", milliseconds)); err != nil {
		return nil, nil, errors.Join(err, release())
	}
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, errors.Join(err, release())
	}
	cleanup := func() error {
		rollbackErr := transaction.Rollback()
		if errors.Is(rollbackErr, sql.ErrTxDone) {
			rollbackErr = nil
		}
		return errors.Join(rollbackErr, release())
	}
	return transaction, cleanup, nil
}

const outgoingColumns = `operation_id,authorization_epoch,peer,reply_to,payload_digest,random_id,state,message_id,sent_at,failure_code,retry_at,created_at,updated_at,retain_until`

type outgoingScanner interface{ Scan(...any) error }

func scanOutgoing(row outgoingScanner) (OutgoingOperation, error) {
	var operation OutgoingOperation
	var peer, createdAt, updatedAt, retainUntil string
	var replyTo, messageID, sentAt, failureCode, retryAt sql.NullString
	var digest []byte
	err := row.Scan(&operation.OperationID, &operation.AuthorizationEpoch, &peer, &replyTo, &digest, &operation.RandomID, &operation.State, &messageID, &sentAt, &failureCode, &retryAt, &createdAt, &updatedAt, &retainUntil)
	if err != nil {
		return OutgoingOperation{}, err
	}
	operation.Peer, err = model.ParsePeerID(peer)
	if err != nil || len(digest) != len(operation.PayloadDigest) {
		return OutgoingOperation{}, ErrInvalidOutgoingState
	}
	copy(operation.PayloadDigest[:], digest)
	if replyTo.Valid {
		value, parseErr := model.ParseMessageID(replyTo.String)
		if parseErr != nil || value.Peer() != operation.Peer {
			return OutgoingOperation{}, ErrInvalidOutgoingState
		}
		operation.ReplyTo = &value
	}
	if messageID.Valid {
		value, parseErr := model.ParseMessageID(messageID.String)
		if parseErr != nil || value.Peer() != operation.Peer {
			return OutgoingOperation{}, ErrInvalidOutgoingState
		}
		operation.MessageID = &value
	}
	for _, field := range []struct {
		value string
		out   *time.Time
	}{{createdAt, &operation.CreatedAt}, {updatedAt, &operation.UpdatedAt}, {retainUntil, &operation.RetainUntil}} {
		*field.out, err = outgoingTime(field.value)
		if err != nil {
			return OutgoingOperation{}, err
		}
	}
	if sentAt.Valid {
		value, parseErr := outgoingTime(sentAt.String)
		if parseErr != nil {
			return OutgoingOperation{}, parseErr
		}
		operation.SentAt = &value
	}
	if retryAt.Valid {
		value, parseErr := outgoingTime(retryAt.String)
		if parseErr != nil {
			return OutgoingOperation{}, parseErr
		}
		operation.RetryAt = &value
	}
	if failureCode.Valid {
		operation.FailureCode = model.SendFailureCode(failureCode.String)
	}
	if err := validateOutgoing(operation); err != nil {
		return OutgoingOperation{}, err
	}
	return operation, nil
}

func outgoingTime(value string) (time.Time, error) {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || at.IsZero() || !strings.HasSuffix(value, "Z") {
		return time.Time{}, ErrInvalidOutgoingState
	}
	return at.UTC(), nil
}

func validateOutgoing(operation OutgoingOperation) error {
	created, err := model.ParseOperationID(operation.OperationID)
	if err != nil || !epochPattern.MatchString(operation.AuthorizationEpoch) || operation.Peer.String() == "" || operation.RandomID == 0 || !created.Equal(operation.CreatedAt) || operation.UpdatedAt.IsZero() || !operation.RetainUntil.Equal(created.Add(OutgoingRetention)) {
		return ErrInvalidOutgoingState
	}
	if operation.ReplyTo != nil && (operation.ReplyTo.String() == "" || operation.ReplyTo.Peer() != operation.Peer) {
		return ErrInvalidOutgoingState
	}
	switch operation.State {
	case model.SendNotSent, model.SendInFlight, model.SendUncertain:
		if operation.MessageID != nil || operation.SentAt != nil {
			return ErrInvalidOutgoingState
		}
	case model.SendSent:
		if operation.MessageID == nil || operation.MessageID.Peer() != operation.Peer || operation.MessageID.String() == "" {
			return ErrInvalidOutgoingState
		}
	default:
		return ErrInvalidOutgoingState
	}
	if operation.Public().Validate() != nil || (operation.SentAt != nil && operation.SentAt.IsZero()) || (operation.RetryAt != nil && operation.RetryAt.IsZero()) {
		return ErrInvalidOutgoingState
	}
	return nil
}

func validSendFailure(code model.SendFailureCode) bool {
	return code == "" || code.IsValid()
}

// Find is read-only and does not require authorization or a running daemon.
// The caller decides which retained epoch it may disclose.
func (r *OutgoingRepository) Find(ctx context.Context, operationID string, now time.Time) (OutgoingOperation, bool, error) {
	if _, err := model.ParseOperationID(operationID); err != nil || now.IsZero() {
		return OutgoingOperation{}, false, ErrInvalidOutgoingState
	}
	operation, err := scanOutgoing(r.database.QueryRowContext(ctx, "SELECT "+outgoingColumns+" FROM outgoing_operations WHERE operation_id=?", operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return OutgoingOperation{}, false, nil
	}
	if err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("read outgoing operation: %w", err)
	}
	if !now.Before(operation.RetainUntil) {
		return OutgoingOperation{}, false, nil
	}
	return operation, true, nil
}

// Admit binds an operation ID to its epoch and keyed payload. A duplicate
// returns the original random ID rather than accepting the candidate's ID.
func (r *OutgoingRepository) Admit(ctx context.Context, candidate OutgoingOperation, now time.Time) (operation OutgoingOperation, createdNew bool, resultErr error) {
	created, err := model.ParseOperationID(candidate.OperationID)
	if err != nil || now.IsZero() {
		return OutgoingOperation{}, false, ErrInvalidOutgoingState
	}
	candidate.CreatedAt, candidate.UpdatedAt, candidate.RetainUntil = created, now.UTC(), created.Add(OutgoingRetention)
	if candidate.State == "" {
		candidate.State = model.SendNotSent
	}
	if candidate.State != model.SendNotSent || candidate.MessageID != nil || candidate.SentAt != nil || candidate.FailureCode != "" || candidate.RetryAt != nil || validateOutgoing(candidate) != nil {
		return OutgoingOperation{}, false, ErrInvalidOutgoingState
	}
	tx, cleanup, err := r.beginWrite(ctx)
	if err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("begin outgoing admission: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, cleanup()) }()
	existing, err := scanOutgoing(tx.QueryRowContext(ctx, "SELECT "+outgoingColumns+" FROM outgoing_operations WHERE operation_id=?", candidate.OperationID))
	if err == nil {
		if existing.AuthorizationEpoch != candidate.AuthorizationEpoch || existing.Peer != candidate.Peer || !sameMessage(existing.ReplyTo, candidate.ReplyTo) || subtle.ConstantTimeCompare(existing.PayloadDigest[:], candidate.PayloadDigest[:]) != 1 {
			return OutgoingOperation{}, false, ErrOutgoingConflict
		}
		if !now.Before(existing.RetainUntil) {
			return OutgoingOperation{}, false, ErrOutgoingExpired
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OutgoingOperation{}, false, fmt.Errorf("inspect outgoing admission: %w", err)
	}
	var currentEpoch string
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM authorization_state WHERE singleton=1").Scan(&currentEpoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OutgoingOperation{}, false, ErrOutgoingUnavailable
		}
		return OutgoingOperation{}, false, fmt.Errorf("read outgoing authorization epoch: %w", err)
	}
	if currentEpoch != candidate.AuthorizationEpoch {
		return OutgoingOperation{}, false, ErrOutgoingUnavailable
	}
	if err := model.ValidateOperationWindow(candidate.OperationID, now); err != nil {
		return OutgoingOperation{}, false, ErrOutgoingExpired
	}
	if err := r.prune(ctx, tx, now); err != nil {
		return OutgoingOperation{}, false, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM outgoing_operations").Scan(&count); err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("count outgoing operations: %w", err)
	}
	if count >= r.capacity {
		return OutgoingOperation{}, false, ErrOutgoingCapacity
	}
	var randomExists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM outgoing_operations WHERE random_id=?", candidate.RandomID).Scan(&randomExists); err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("inspect outgoing random identifier: %w", err)
	}
	if randomExists != 0 {
		return OutgoingOperation{}, false, ErrOutgoingConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outgoing_operations (`+outgoingColumns+`) VALUES (?,?,?,?,?,?,'not_sent',NULL,NULL,NULL,NULL,?,?,?)`, candidate.OperationID, candidate.AuthorizationEpoch, candidate.Peer.String(), nullableMessage(candidate.ReplyTo), candidate.PayloadDigest[:], candidate.RandomID, outgoingTimestamp(created), outgoingTimestamp(now), outgoingTimestamp(candidate.RetainUntil))
	if err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("admit outgoing operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("commit outgoing admission: %w", err)
	}
	return candidate, true, nil
}

func sameMessage(a, b *model.MessageID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func nullableMessage(id *model.MessageID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

func outgoingTimestamp(at time.Time) string { return at.UTC().Format(metadataTimeLayout) }

func nullableTime(at *time.Time) any {
	if at == nil {
		return nil
	}
	return outgoingTimestamp(*at)
}

// BeginDispatch durably fences the only transition that may initiate a send.
// Its transaction is complete before the caller performs Telegram I/O.
func (r *OutgoingRepository) BeginDispatch(ctx context.Context, operationID, epoch string, now time.Time) (OutgoingOperation, bool, error) {
	operation, changed, err := r.change(ctx, operationID, epoch, now, true, func(operation *OutgoingOperation) (bool, error) {
		if operation.State != model.SendNotSent {
			return false, nil
		}
		if err := model.ValidateOperationWindow(operationID, now); err != nil {
			return false, ErrOutgoingExpired
		}
		if operation.RetryAt != nil && now.Before(*operation.RetryAt) {
			return false, ErrOutgoingWait
		}
		operation.State, operation.FailureCode, operation.RetryAt = model.SendInFlight, "", nil
		return true, nil
	})
	return operation, changed, err
}

func (r *OutgoingRepository) RecordNotSent(ctx context.Context, operationID, epoch string, code model.SendFailureCode, retryAt *time.Time, now time.Time) (OutgoingOperation, error) {
	operation, _, err := r.recordFailure(ctx, operationID, epoch, model.SendNotSent, code, retryAt, now)
	return operation, err
}

func (r *OutgoingRepository) RecordUncertain(ctx context.Context, operationID, epoch string, code model.SendFailureCode, retryAt *time.Time, now time.Time) (OutgoingOperation, error) {
	operation, _, err := r.recordFailure(ctx, operationID, epoch, model.SendUncertain, code, retryAt, now)
	return operation, err
}

func (r *OutgoingRepository) recordFailure(ctx context.Context, operationID, epoch string, state model.SendState, code model.SendFailureCode, retryAt *time.Time, now time.Time) (OutgoingOperation, bool, error) {
	if !validSendFailure(code) || (retryAt != nil && retryAt.IsZero()) {
		return OutgoingOperation{}, false, ErrInvalidOutgoingState
	}
	return r.change(ctx, operationID, epoch, now, false, func(operation *OutgoingOperation) (bool, error) {
		if operation.State == model.SendSent || (operation.State == model.SendUncertain && state == model.SendNotSent) {
			return false, nil
		}
		operation.State, operation.FailureCode, operation.RetryAt = state, code, retryAt
		return true, nil
	})
}

func (r *OutgoingRepository) change(ctx context.Context, operationID, epoch string, now time.Time, requireCurrent bool, mutate func(*OutgoingOperation) (bool, error)) (result OutgoingOperation, changedResult bool, resultErr error) {
	if _, err := model.ParseOperationID(operationID); err != nil || !epochPattern.MatchString(epoch) || now.IsZero() {
		return OutgoingOperation{}, false, ErrInvalidOutgoingState
	}
	tx, cleanup, err := r.beginWrite(ctx)
	if err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("begin outgoing update: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, cleanup()) }()
	operation, err := scanOutgoing(tx.QueryRowContext(ctx, "SELECT "+outgoingColumns+" FROM outgoing_operations WHERE operation_id=?", operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return OutgoingOperation{}, false, ErrOutgoingUnavailable
	}
	if err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("read outgoing update: %w", err)
	}
	if operation.AuthorizationEpoch != epoch {
		return OutgoingOperation{}, false, ErrOutgoingConflict
	}
	if !now.Before(operation.RetainUntil) {
		return OutgoingOperation{}, false, ErrOutgoingUnavailable
	}
	if requireCurrent {
		var currentEpoch string
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM authorization_state WHERE singleton=1").Scan(&currentEpoch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return OutgoingOperation{}, false, ErrOutgoingUnavailable
			}
			return OutgoingOperation{}, false, fmt.Errorf("read outgoing dispatch authorization: %w", err)
		}
		if currentEpoch != epoch {
			return OutgoingOperation{}, false, ErrOutgoingUnavailable
		}
	}
	changed, err := mutate(&operation)
	if err != nil || !changed {
		return operation, false, err
	}
	operation.UpdatedAt = now.UTC()
	if err := validateOutgoing(operation); err != nil {
		return OutgoingOperation{}, false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE outgoing_operations SET state=?,message_id=?,sent_at=?,failure_code=?,retry_at=?,updated_at=? WHERE operation_id=? AND authorization_epoch=?`, operation.State, nullableMessage(operation.MessageID), nullableTime(operation.SentAt), nullableCode(operation.FailureCode), nullableTime(operation.RetryAt), outgoingTimestamp(now), operationID, epoch)
	if err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("update outgoing operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("commit outgoing update: %w", err)
	}
	return operation, true, nil
}

func nullableCode(code model.SendFailureCode) any {
	if code == "" {
		return nil
	}
	return string(code)
}

// RecordReceipt records a positive fact only for an exact existing epoch/random
// ID pair. It acquires no policy or account lease, including when called by the
// update manager while the sending caller still holds its policy lease.
func (r *OutgoingRepository) RecordReceipt(ctx context.Context, epoch string, randomID int64, messageID int32, sentAt *time.Time, now time.Time) (OutgoingOperation, bool, error) {
	if !epochPattern.MatchString(epoch) || randomID == 0 || messageID <= 0 || now.IsZero() || (sentAt != nil && sentAt.IsZero()) {
		return OutgoingOperation{}, false, ErrInvalidOutgoingState
	}
	var operationID string
	err := r.database.QueryRowContext(ctx, "SELECT operation_id FROM outgoing_operations WHERE authorization_epoch=? AND random_id=?", epoch, randomID).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return OutgoingOperation{}, false, nil
	}
	if err != nil {
		return OutgoingOperation{}, false, fmt.Errorf("match outgoing receipt: %w", err)
	}
	operation, _, err := r.change(ctx, operationID, epoch, now, false, func(operation *OutgoingOperation) (bool, error) {
		id, err := model.NewMessageID(operation.Peer, messageID)
		if err != nil {
			return false, ErrInvalidOutgoingState
		}
		if operation.MessageID != nil {
			if *operation.MessageID != id || (operation.SentAt != nil && sentAt != nil && !operation.SentAt.Equal(*sentAt)) {
				return false, ErrOutgoingConflict
			}
			if operation.SentAt != nil || sentAt == nil {
				return false, nil
			}
		}
		operation.State, operation.MessageID, operation.SentAt, operation.FailureCode, operation.RetryAt = model.SendSent, &id, sentAt, "", nil
		return true, nil
	})
	if errors.Is(err, ErrOutgoingUnavailable) {
		return OutgoingOperation{}, false, nil
	}
	return operation, err == nil, err
}

// RecoverInterrupted must complete before a newly started account can send.
// It never retries an RPC; interrupted dispatches retain their random IDs.
func (r *OutgoingRepository) RecoverInterrupted(ctx context.Context, now time.Time) (resultErr error) {
	if now.IsZero() {
		return ErrInvalidOutgoingState
	}
	tx, cleanup, err := r.beginWrite(ctx)
	if err != nil {
		return fmt.Errorf("begin outgoing recovery: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, cleanup()) }()
	if err := r.prune(ctx, tx, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outgoing_operations SET state='uncertain',failure_code=?,retry_at=NULL,updated_at=? WHERE state='in_flight'`, model.SendFailureEffectUncertain, outgoingTimestamp(now)); err != nil {
		return fmt.Errorf("recover interrupted outgoing operations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outgoing recovery: %w", err)
	}
	return nil
}

func (r *OutgoingRepository) prune(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM outgoing_operations WHERE operation_id IN (SELECT operation_id FROM outgoing_operations WHERE retain_until<=? ORDER BY retain_until LIMIT ?)`, outgoingTimestamp(now), r.pruneLimit); err != nil {
		return fmt.Errorf("prune outgoing operations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM telegram_rpc_waits WHERE rowid IN (SELECT rowid FROM telegram_rpc_waits WHERE not_before<=? ORDER BY not_before LIMIT ?)`, outgoingTimestamp(now), r.pruneLimit); err != nil {
		return fmt.Errorf("prune Telegram RPC waits: %w", err)
	}
	return nil
}

func validateRPCScope(environment string, accountID int64, method string, peer model.PeerID) (string, error) {
	if environment != ProductionEnvironment && environment != "test:1" && environment != "test:2" && environment != "test:3" {
		return "", ErrInvalidOutgoingState
	}
	if accountID <= 0 {
		return "", ErrInvalidOutgoingState
	}
	switch method {
	case "messages.sendMessage", "contacts.resolveUsername", "contacts.resolvePhone", "users.getUsers", "channels.getChannels", "messages.getChats", "messages.getForumTopicsByID", "channels.getMessages", "messages.getMessages":
	default:
		return "", ErrInvalidOutgoingState
	}
	if peer == (model.PeerID{}) {
		return "", nil
	}
	if peer.String() == "" {
		return "", ErrInvalidOutgoingState
	}
	return peer.Parent().String(), nil
}

// WaitUntil returns the later method-wide or containing-dialog deadline. Forum
// topics share their parent's slow-mode deadline. The identity is supplied only
// after the Telegram adapter has verified the environment and logged-in user.
func (r *OutgoingRepository) WaitUntil(ctx context.Context, environment string, accountID int64, method string, peer model.PeerID, now time.Time) (time.Time, error) {
	scope, err := validateRPCScope(environment, accountID, method, peer)
	if err != nil || now.IsZero() {
		return time.Time{}, ErrInvalidOutgoingState
	}
	rows, err := r.database.QueryContext(ctx, `SELECT not_before FROM telegram_rpc_waits WHERE environment=? AND account_id=? AND method=? AND scope_peer IN ('',?)`, environment, accountID, method, scope)
	if err != nil {
		return time.Time{}, fmt.Errorf("read Telegram RPC wait: %w", err)
	}
	defer rows.Close()
	var deadline time.Time
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return time.Time{}, fmt.Errorf("read Telegram RPC wait: %w", err)
		}
		at, err := outgoingTime(value)
		if err != nil {
			return time.Time{}, err
		}
		if at.After(now) && at.After(deadline) {
			deadline = at
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, fmt.Errorf("read Telegram RPC waits: %w", err)
	}
	return deadline, nil
}

// RecordWait only extends a protocol deadline. It deliberately has no epoch
// parameter: disabling access or logging the same account in again cannot erase
// an observed Telegram restriction.
func (r *OutgoingRepository) RecordWait(ctx context.Context, environment string, accountID int64, method string, peer model.PeerID, notBefore, now time.Time) (resultErr error) {
	scope, err := validateRPCScope(environment, accountID, method, peer)
	if err != nil || now.IsZero() || notBefore.IsZero() {
		return ErrInvalidOutgoingState
	}
	tx, cleanup, err := r.beginWrite(ctx)
	if err != nil {
		return fmt.Errorf("begin Telegram RPC wait: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, cleanup()) }()
	if err := r.saveWait(ctx, tx, environment, accountID, method, scope, notBefore, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Telegram RPC wait: %w", err)
	}
	return nil
}

func (r *OutgoingRepository) saveWait(ctx context.Context, tx *sql.Tx, environment string, accountID int64, method, scope string, notBefore, now time.Time) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT not_before FROM telegram_rpc_waits WHERE environment=? AND account_id=? AND method=? AND scope_peer=?`, environment, accountID, method, scope).Scan(&existing)
	if err == nil {
		at, err := outgoingTime(existing)
		if err != nil {
			return err
		}
		if !notBefore.After(at) {
			return nil
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if !notBefore.After(now) {
			return nil
		}
		if err := r.prune(ctx, tx, now); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM telegram_rpc_waits").Scan(&count); err != nil {
			return fmt.Errorf("count Telegram RPC waits: %w", err)
		}
		if count >= r.capacity {
			return ErrOutgoingCapacity
		}
	} else {
		return fmt.Errorf("inspect Telegram RPC wait: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO telegram_rpc_waits(environment,account_id,method,scope_peer,not_before) VALUES (?,?,?,?,?) ON CONFLICT(environment,account_id,method,scope_peer) DO UPDATE SET not_before=excluded.not_before`, environment, accountID, method, scope, outgoingTimestamp(notBefore)); err != nil {
		return fmt.Errorf("save Telegram RPC wait: %w", err)
	}
	return nil
}

// ReservePhoneLookup atomically consumes the account's next three-second slot.
// A blocked lookup returns the current deadline without extending it. No network
// call occurs in the transaction, and callers invoke Telegram only when acquired.
// The local slot is separate from Telegram's method cooldown, so reserving a
// lookup does not cause its own RPC to fail the provider-wait check.
func (r *OutgoingRepository) ReservePhoneLookup(ctx context.Context, environment string, accountID int64, now time.Time) (notBefore time.Time, acquired bool, err error) {
	if _, err := validateRPCScope(environment, accountID, "contacts.resolvePhone", model.PeerID{}); err != nil || now.IsZero() {
		return time.Time{}, false, ErrInvalidOutgoingState
	}
	tx, cleanup, err := r.beginWrite(ctx)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("begin phone lookup reservation: %w", err)
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	var value string
	err = tx.QueryRowContext(ctx, `SELECT not_before FROM telegram_rpc_waits WHERE environment=? AND account_id=? AND method=? AND scope_peer=''`, environment, accountID, phoneLookupSlot).Scan(&value)
	if err == nil {
		at, parseErr := outgoingTime(value)
		if parseErr != nil {
			return time.Time{}, false, parseErr
		}
		if now.Before(at) {
			return at, false, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, fmt.Errorf("inspect phone lookup reservation: %w", err)
	}
	if err := r.saveWait(ctx, tx, environment, accountID, phoneLookupSlot, "", now.Add(phoneLookupInterval), now); err != nil {
		return time.Time{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, false, fmt.Errorf("commit phone lookup reservation: %w", err)
	}
	return now.UTC(), true, nil
}
