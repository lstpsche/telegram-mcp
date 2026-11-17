// Package sender coordinates account-authorized, individual Telegram sends.
// It retains outcome metadata, never message bodies or recipient aliases.
package sender

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

const (
	OperationTimeout = 20 * time.Second
	metadataGrace    = 5 * time.Second
)

// Backend exposes normalized Telegram operations without generated API types.
type Backend interface {
	Ready() bool
	ResolveRecipient(context.Context, model.ResolveRecipientQuery) (model.ResolvedRecipient, error)
	PrepareSend(context.Context, model.SendAttempt) (model.PreparedSend, error)
}

// Result keeps a known operation's facts even when the invocation also failed.
// JSON is mirrored verbatim in both MCP result representations.
type Result struct {
	JSON    json.RawMessage
	IsError bool
}

type Service struct {
	backend      Backend
	policy       *policy.Repository
	operations   *store.OutgoingRepository
	now          func() time.Time
	random       io.Reader
	integrityKey [32]byte
	fenced       atomic.Bool
}

func New(backend Backend, repository *policy.Repository, operations *store.OutgoingRepository, now func() time.Time, random io.Reader, integrityKey []byte) (*Service, error) {
	if backend == nil || repository == nil || operations == nil || now == nil || random == nil || len(integrityKey) != 32 {
		return nil, errors.New("sending service dependencies are required")
	}
	s := &Service{backend: backend, policy: repository, operations: operations, now: now, random: random}
	copy(s.integrityKey[:], integrityKey)
	return s, nil
}

func (s *Service) Ready() bool {
	return s != nil && !s.fenced.Load() && s.backend.Ready()
}

func requireSend(ctx context.Context, lease *policy.Lease) error {
	enabled, err := lease.FullSend(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return model.TextError(model.ErrorPolicyDenied, nil)
	}
	return nil
}

func (s *Service) ResolveRecipient(ctx context.Context, requestID string, query model.ResolveRecipientQuery) (result Result, resultErr error) {
	if s == nil {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	if !model.IsValidRequestID(requestID) {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	query, err := query.Normalize()
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	lease, err := s.policy.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if err := lease.Close(); err != nil {
			result = Result{}
			resultErr = model.TextError(model.ErrorInternal, errors.Join(resultErr, err))
		}
	}()
	if err := requireSend(ctx, lease); err != nil {
		return Result{}, err
	}
	if !s.Ready() {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	recipient, err := s.backend.ResolveRecipient(ctx, query)
	if err != nil {
		if errors.Is(err, model.ErrSendMetadataRecording) {
			s.fenced.Store(true)
		}
		return prepareError(requestID, safeError(err))
	}
	if err := recipient.Validate(); err != nil || recipient.OperationID != "" || (query.Peer.String() != "" && recipient.Peer != query.Peer) {
		return Result{}, model.TextError(model.ErrorRecipientUnavailable, err)
	}
	if err := requireSend(ctx, lease); err != nil {
		return Result{}, err
	}
	recipient.OperationID, err = model.NewOperationID(s.now(), s.random)
	if err != nil {
		return Result{}, safeError(err)
	}
	return prepare(requestID, recipient, s.now(), model.FreshnessLive, nil)
}

func (s *Service) SendStatus(ctx context.Context, requestID, operationID string) (result Result, resultErr error) {
	if s == nil {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	if !model.IsValidRequestID(requestID) {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if _, err := model.ParseOperationID(operationID); err != nil {
		return Result{}, model.TextError(model.ErrorInvalidInput, err)
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	lease, err := s.policy.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		result, resultErr = closeLease(lease, result, resultErr)
	}()
	if err := requireSend(ctx, lease); err != nil {
		return Result{}, err
	}
	epoch, _, err := lease.Binding(ctx)
	if err != nil {
		return Result{}, err
	}
	operation, found, err := s.operations.Find(ctx, operationID, s.now())
	if err != nil {
		return Result{}, safeError(err)
	}
	if !found || operation.AuthorizationEpoch != epoch {
		return Result{}, model.TextError(model.ErrorOperationUnavailable, nil)
	}
	if err := requireSend(ctx, lease); err != nil {
		return Result{}, err
	}
	return prepare(requestID, operation.Public(), s.now(), model.FreshnessUnavailable, nil)
}

func samePayload(operation store.OutgoingOperation, request model.SendRequest, digest [32]byte) bool {
	if operation.Peer != request.Peer || !hmac.Equal(operation.PayloadDigest[:], digest[:]) {
		return false
	}
	if operation.ReplyTo == nil || request.ReplyTo == nil {
		return operation.ReplyTo == nil && request.ReplyTo == nil
	}
	return *operation.ReplyTo == *request.ReplyTo
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	return model.TextError(model.TextErrorCategory(err), err)
}

func closeLease(lease *policy.Lease, result Result, resultErr error) (Result, error) {
	if err := lease.Close(); err != nil {
		resultErr = model.TextError(model.ErrorInternal, errors.Join(resultErr, err))
		if len(result.JSON) > 0 {
			updated, err := addResultError(result, resultErr)
			if err != nil {
				result.IsError = true
				return result, model.TextError(model.ErrorInternal, errors.Join(resultErr, err))
			}
			result = updated
		}
	}
	return result, resultErr
}
