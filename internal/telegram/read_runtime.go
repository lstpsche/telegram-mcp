package telegram

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/log"
	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	gotdauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/telegram/updates/hook"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"golang.org/x/sync/errgroup"
)

const readDeadline = 15 * time.Second

// A runtime fails closed for its remaining lifetime after checkpoint or update
// recovery errors. Restarting constructs a fresh manager from durable metadata.
type readRuntime struct {
	manager          *updates.Manager
	storage          *readStorage
	api              *tg.Client
	self             atomic.Int64
	ready            atomic.Bool
	observed         atomic.Bool
	mu               sync.Mutex
	failure          error
	recoveryCalls    int
	startupMarker    *tg.UpdatePrivacy
	startupProcessed chan struct{}
	startupOnce      sync.Once
	failed           chan struct{}
	failureOnce      sync.Once
}

func (r *readRuntime) fail(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.failure == nil || (gotdauth.IsUnauthorized(err) && !gotdauth.IsUnauthorized(r.failure)) {
		r.failure = err
	}
	r.mu.Unlock()
	r.ready.Store(false)
	r.failureOnce.Do(func() { close(r.failed) })
}
func (r *readRuntime) err() error { r.mu.Lock(); defer r.mu.Unlock(); return r.failure }

func (a *Account) EnableReads(ctx context.Context, database *sql.DB, epoch string) error {
	if a == nil || a.reads == nil || database == nil || len(epoch) < 16 || len(epoch) > 128 {
		return model.TextError(model.ErrorNotReady, errors.New("read runtime configuration is invalid"))
	}
	r := a.reads
	if r.storage != nil {
		return model.TextError(model.ErrorNotReady, errors.New("read runtime is already configured"))
	}
	r.storage = &readStorage{db: database, epoch: epoch, runtime: r}
	if err := r.storage.checkEpoch(ctx); err != nil {
		return model.TextError(model.ErrorNotReady, err)
	}
	r.api = a.client.API()
	r.startupMarker = &tg.UpdatePrivacy{Key: &tg.PrivacyKeyStatusTimestamp{}}
	r.startupProcessed = make(chan struct{})
	r.manager = updates.New(updates.Config{
		Handler: gotdtelegram.UpdateHandlerFunc(r.discardUpdates),
		Storage: r.storage, AccessHasher: r.storage, UserAccessHasher: r.storage,
		Logger: readUpdateLog{r}, MaxChannelDifferenceConcurrency: 1,
		OnTooLong:                func() { r.fail(errors.New("Telegram common update gap is unrecoverable")) },
		OnChannelTooLong:         func(int64) { r.fail(errors.New("Telegram channel update gap is unrecoverable")) },
		OnChannelInaccessible:    func(int64) { r.fail(errors.New("Telegram channel access was lost")) },
		OnLoadUserStateFailed:    func() { r.fail(errors.New("Telegram update checkpoint is missing")) },
		OnLoadChannelStateFailed: func(int64) { r.fail(errors.New("Telegram channel checkpoint is incomplete")) },
	})
	return nil
}

func (a *Account) Ready() bool {
	return a != nil && a.reads != nil && a.reads.ready.Load() && a.reads.err() == nil
}
func (a *Account) SelfID() model.PeerID {
	if a == nil || a.reads == nil {
		return model.PeerID{}
	}
	id, _ := model.NewPeerID(model.PeerKindUser, a.reads.self.Load())
	return id
}

func (a *Account) observeReads(ctx context.Context, callback func(context.Context, AuthorizationStatus) error) error {
	r := a.reads
	if r.storage == nil || r.manager == nil {
		return model.TextError(model.ErrorNotReady, nil)
	}
	self, err := a.client.Self(ctx)
	if err != nil {
		return model.TextError(model.ErrorTelegramUnavailable, err)
	}
	if self.Bot || self.Deleted || self.ID <= 0 {
		return model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	r.self.Store(self.ID)
	initial, cancel := context.WithTimeout(ctx, readDeadline)
	_, found, err := r.storage.GetState(initial, self.ID)
	if err == nil && !found {
		var state *tg.UpdatesState
		state, err = r.UpdatesGetState(initial)
		if err == nil {
			err = r.storage.SetState(initial, self.ID, updates.State{Pts: state.Pts, Qts: state.Qts, Date: state.Date, Seq: state.Seq})
		}
	}
	cancel()
	if err != nil {
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return r.observe(ctx, callback)
}

func (r *readRuntime) observe(ctx context.Context, callback func(context.Context, AuthorizationStatus) error) error {
	if !r.observed.CompareAndSwap(false, true) {
		return model.TextError(model.ErrorNotReady, errors.New("read runtime already observed an account session"))
	}
	group, runCtx := errgroup.WithContext(ctx)
	runCtx, stop := context.WithCancel(runCtx)
	defer stop()
	started := make(chan struct{})
	group.Go(func() error {
		select {
		case <-r.failed:
			return r.err()
		case <-runCtx.Done():
			return runCtx.Err()
		}
	})
	group.Go(func() error {
		defer r.ready.Store(false)
		defer r.manager.Reset()
		err := r.manager.Run(runCtx, r, r.self.Load(), updates.AuthOptions{OnStart: func(context.Context) { close(started) }})
		if err != nil && !errors.Is(err, context.Canceled) {
			r.fail(err)
		}
		return err
	})
	var callbackError error
	group.Go(func() error {
		select {
		case <-started:
		case <-runCtx.Done():
			return runCtx.Err()
		}
		bounded, cancel := context.WithTimeout(runCtx, readDeadline)
		defer cancel()
		if err := r.awaitStartup(bounded); err != nil {
			return model.TextError(model.ErrorFreshnessDegraded, err)
		}
		r.ready.Store(true)
		callbackError = callback(runCtx, AuthorizationStatus{Authorized: true})
		stop()
		return callbackError
	})
	err := group.Wait()
	if failure := r.err(); failure != nil && gotdauth.IsUnauthorized(failure) {
		return readError(failure)
	}
	if callbackError != nil && !errors.Is(callbackError, context.Canceled) {
		return callbackError
	}
	if failure := r.err(); failure != nil {
		return model.TextError(model.ErrorFreshnessDegraded, failure)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// Handle never sends Telegram-derived fields to a logger or public model.
func (r *readRuntime) Handle(ctx context.Context, u tg.UpdatesClass) error {
	if r.manager == nil {
		return model.TextError(model.ErrorNotReady, nil)
	}
	if err := r.err(); err != nil {
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	projected, err := commonUpdates(u)
	if err != nil {
		r.fail(err)
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return r.manager.Handle(ctx, projected)
}

type readMiddleware struct{ runtime *readRuntime }

func (m readMiddleware) Handle(next tg.Invoker) gotdtelegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		r := m.runtime
		if r.manager == nil {
			return model.TextError(model.ErrorNotReady, nil)
		}
		if err := r.err(); err != nil {
			return model.TextError(model.ErrorFreshnessDegraded, err)
		}
		if err := ctx.Err(); err != nil {
			return readError(err)
		}
		err := hook.UpdateHook(r.Handle).Handle(hook.AffectedHook(r.manager).Handle(next)).Invoke(ctx, input, output)
		if gotdauth.IsUnauthorized(err) {
			r.fail(err)
			return readError(err)
		}
		return err
	}
}

// gotd occasionally only logs synchronization failures. Capture a fixed cause;
// never forward its message or attributes, which may contain hostile data.
type readUpdateLog struct{ runtime *readRuntime }

func (l readUpdateLog) Enabled(_ context.Context, level log.Level) bool {
	return level >= log.LevelWarn
}
func (l readUpdateLog) Log(_ context.Context, level log.Level, _ string, _ ...log.Attr) {
	if level >= log.LevelWarn {
		l.runtime.fail(errors.New("Telegram update processing failed"))
	}
}

func (r *readRuntime) UpdatesGetState(ctx context.Context) (*tg.UpdatesState, error) {
	if err := r.err(); err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	result, err := r.api.UpdatesGetState(bounded)
	if err == nil && !validRemoteState(result) {
		err = errors.New("Telegram update state is invalid")
	}
	if err != nil {
		r.fail(err)
	}
	return result, err
}
func (r *readRuntime) UpdatesGetDifference(ctx context.Context, q *tg.UpdatesGetDifferenceRequest) (tg.UpdatesDifferenceClass, error) {
	if err := r.err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.recoveryCalls++
	calls := r.recoveryCalls
	r.mu.Unlock()
	if calls > 8 {
		err := errors.New("Telegram update recovery exceeds the RPC budget")
		r.fail(err)
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	copy := *q
	copy.SetPtsLimit(100)
	copy.SetPtsTotalLimit(800)
	copy.SetQtsLimit(100)
	result, err := r.api.UpdatesGetDifference(bounded, &copy)
	if err == nil {
		var checkpoint updates.State
		var found bool
		checkpoint, found, err = r.storage.GetState(bounded, r.self.Load())
		if err == nil && !found {
			err = errors.New("Telegram update checkpoint is missing")
		}
		if err == nil {
			err = validateDifference(q, checkpoint.Seq, result)
		}
	}
	if err == nil {
		result, err = commonDifference(result)
	}
	if err == nil {
		// The manager skips hashes it already knows. A recovered full entity
		// must also replace an older hash before the checkpoint can advance.
		switch value := result.(type) {
		case *tg.UpdatesDifference:
			err = r.saveUsers(bounded, value.Users)
		case *tg.UpdatesDifferenceSlice:
			err = r.saveUsers(bounded, value.Users)
		}
	}
	if err != nil {
		r.fail(err)
		return nil, err
	}
	switch result.(type) {
	case *tg.UpdatesDifference, *tg.UpdatesDifferenceEmpty:
		r.mu.Lock()
		r.recoveryCalls = 0
		r.mu.Unlock()
	}
	return result, nil
}
func (r *readRuntime) UpdatesGetChannelDifference(ctx context.Context, q *tg.UpdatesGetChannelDifferenceRequest) (tg.UpdatesChannelDifferenceClass, error) {
	// The adapter does not subscribe to channel updates or maintain a channel
	// cache. An unexpected recovery request must fail, never fabricate state.
	err := errors.New("Telegram channel recovery is unsupported")
	r.fail(err)
	return nil, err
}

func (r *readRuntime) synchronize(ctx context.Context) error {
	state, err := r.UpdatesGetState(ctx)
	if err != nil {
		return err
	}
	return r.waitCheckpoint(ctx, state.Pts, state.Qts, state.Seq)
}
func (r *readRuntime) waitCheckpoint(ctx context.Context, pts, qts, seq int) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := r.err(); err != nil {
			return err
		}
		state, found, err := r.storage.GetState(ctx, r.self.Load())
		if err != nil {
			return err
		}
		if !found {
			return errors.New("Telegram checkpoint is missing")
		}
		if state.Pts >= pts && state.Qts >= qts && state.Seq >= seq {
			if err := r.err(); err != nil {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (a *Account) Acknowledge(ctx context.Context, peer model.PeerID, through int32) error {
	if !a.Ready() {
		return model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	if through <= 0 {
		return model.TextError(model.ErrorInvalidInput, nil)
	}
	// Refresh peer protection/bot membership immediately before a read effect.
	if _, err := a.Chat(bounded, peer); err != nil {
		return err
	}
	input, err := a.reads.inputPeer(bounded, peer)
	if err != nil {
		return err
	}
	if channel, ok := input.(*tg.InputPeerChannel); ok {
		return a.acknowledgeSupergroup(bounded, peer, channel, through)
	}
	result, err := a.reads.api.MessagesReadHistory(bounded, &tg.MessagesReadHistoryRequest{Peer: input, MaxID: int(through)})
	if err != nil {
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	if result.Pts < 0 || result.PtsCount < 0 || result.PtsCount > result.Pts {
		return model.TextError(model.ErrorFreshnessDegraded, errors.New("Telegram read receipt is invalid"))
	}
	// The affected hook enqueues work; durable storage, not enqueue success, is
	// the receipt completion boundary. A zero pts receipt still requires a live
	// state fetch and synchronization rather than assuming no updates are pending.
	if err := a.reads.waitCheckpoint(bounded, result.Pts, 0, 0); err != nil {
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	if err := a.reads.synchronize(bounded); err != nil {
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return nil
}

func readError(err error) error {
	var operation *model.OperationError
	if errors.As(err, &operation) {
		return operation
	}
	if gotdauth.IsUnauthorized(err) {
		return model.TextError(model.ErrorReauthRequired, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return model.TextError(model.ErrorCancelled, err)
	}
	return model.TextError(model.ErrorTelegramUnavailable, err)
}

// A local, pointer-identified marker carries no sequence or server state. The
// manager processes its external queue only after initial difference recovery.
// Waiting for this marker prevents equal saved/remote checkpoints from making
// an account ready while that recovery is still running or has failed.
func (r *readRuntime) awaitStartup(ctx context.Context) error {
	if err := r.Handle(ctx, &tg.UpdateShort{Update: r.startupMarker}); err != nil {
		return err
	}
	select {
	case <-r.startupProcessed:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := r.err(); err != nil {
		return err
	}
	return r.synchronize(ctx)
}

func (r *readRuntime) discardUpdates(_ context.Context, value tg.UpdatesClass) error {
	if batch, ok := value.(*tg.Updates); ok {
		for _, update := range batch.Updates {
			if update == r.startupMarker {
				r.startupOnce.Do(func() { close(r.startupProcessed) })
			}
		}
	}
	return nil
}

func validRemoteState(state *tg.UpdatesState) bool {
	return state != nil && state.Pts >= 0 && state.Qts >= 0 && state.Seq >= 0 && state.Date > 0
}

func validateDifference(request *tg.UpdatesGetDifferenceRequest, seq int, result tg.UpdatesDifferenceClass) error {
	var state *tg.UpdatesState
	var messages, other, users, chats int
	switch value := result.(type) {
	case *tg.UpdatesDifference:
		state = &value.State
		messages, other, users, chats = len(value.NewMessages)+len(value.NewEncryptedMessages), len(value.OtherUpdates), len(value.Users), len(value.Chats)
	case *tg.UpdatesDifferenceSlice:
		state = &value.IntermediateState
		messages, other, users, chats = len(value.NewMessages)+len(value.NewEncryptedMessages), len(value.OtherUpdates), len(value.Users), len(value.Chats)
		if state.Pts == request.Pts && state.Qts == request.Qts && state.Date <= request.Date {
			return errors.New("Telegram update difference made no progress")
		}
	case *tg.UpdatesDifferenceEmpty:
		if value.Date > 0 && value.Seq >= seq {
			return nil
		}
	case *tg.UpdatesDifferenceTooLong:
		// gotd saves this pts before invoking OnTooLong. Never let an
		// unrecovered gap become the starting checkpoint of the next process.
		return errors.New("Telegram common update gap is unrecoverable")
	}
	if !validRemoteState(state) || state.Pts < request.Pts || state.Qts < request.Qts || state.Seq < seq {
		return errors.New("Telegram update difference state is invalid")
	}
	if messages > 100 || other > 100 || users > 200 || chats > 200 {
		return errors.New("Telegram update difference exceeds the item budget")
	}
	return nil
}
