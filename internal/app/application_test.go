package app

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
	metastore "github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testAPIHash = "0123456789abcdef0123456789abcdef"

const (
	signalHelperStateEnvironment   = "TELEGRAM_MCP_TEST_STATE_DIR"
	signalHelperRuntimeEnvironment = "TELEGRAM_MCP_TEST_RUNTIME_DIR"
)

func TestConfigureAuthenticateRestartAndLogoutState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	application, secretStore := newTestApplication(t)
	if err := application.Configure(ctx, tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	if _, ok := secretStore.value(tgaccount.CredentialsSecretAccount); !ok {
		t.Fatal("Configure() did not store the API hash")
	}
	databaseBytes, err := os.ReadFile(application.paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(databaseBytes, []byte(testAPIHash)) {
		t.Fatal("metadata database contains the API hash")
	}
	status, err := application.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.TestDC != 2 || status.Authorized {
		t.Fatalf("Status() after configure = %#v", status)
	}

	runtime := &fakeRuntime{authResult: tgaccount.AuthResult{Performed: true}}
	application.factory = func(config tgaccount.Config, _ *tgaccount.SessionStorage, mode tgaccount.Mode) (accountRuntime, error) {
		if config.APIID != 12345 || config.TestDC != 2 || string(config.APIHash) != testAPIHash {
			t.Fatalf("runtime config = %#v, mode = %d", config, mode)
		}
		return runtime, nil
	}
	outcome, err := application.Authenticate(ctx, tgaccount.AuthMethodPhone, fakePrompt{})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Performed || !runtime.invalidatedBeforeAuth {
		t.Fatalf("Authenticate() outcome = %#v, invalidated = %t", outcome, runtime.invalidatedBeforeAuth)
	}
	firstAuthorization := authorizationState(t, application.paths.Database)
	status, err = application.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authorized || !status.PhoneCheckPassed || status.QRCheckPassed {
		t.Fatalf("Status() after auth = %#v", status)
	}

	runtime.authResult = tgaccount.AuthResult{}
	outcome, err = application.Authenticate(ctx, tgaccount.AuthMethodQR, fakePrompt{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Performed {
		t.Fatal("existing authorized session was reported as a new authentication")
	}
	secondAuthorization := authorizationState(t, application.paths.Database)
	if secondAuthorization.Epoch != firstAuthorization.Epoch {
		t.Fatal("existing session unexpectedly rotated its authorization epoch")
	}
	status, err = application.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.QRCheckPassed {
		t.Fatal("existing session was recorded as a QR authentication check")
	}

	if err := application.Configure(ctx, tgaccount.TestEnvironment, 3, staticConfiguration(54321)); !errors.Is(err, metastore.ErrAuthorizationExists) {
		t.Fatalf("Configure() while authorized error = %v", err)
	}
	secretStore.set(tgaccount.SessionSecretAccount, []byte("session material"))
	if err := application.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := secretStore.value(tgaccount.SessionSecretAccount); ok {
		t.Fatal("Logout() retained the Telegram session")
	}
	if _, ok := secretStore.value(tgaccount.CredentialsSecretAccount); !ok {
		t.Fatal("Logout() removed the API hash needed for explicit reauthentication")
	}
	status, err = application.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Authorized || !status.PhoneCheckPassed {
		t.Fatalf("Status() after logout = %#v", status)
	}
}

func TestConfigureRejectsStoredSessionWithoutAuthorizationEpoch(t *testing.T) {
	t.Parallel()

	application, secretStore := newTestApplication(t)
	secretStore.set(tgaccount.SessionSecretAccount, []byte("session material"))
	readerCalled := false
	err := application.Configure(context.Background(), tgaccount.TestEnvironment, 2, func(context.Context) (int, []byte, error) {
		readerCalled = true
		return 12345, []byte(testAPIHash), nil
	})
	if !errors.Is(err, metastore.ErrAuthorizationExists) {
		t.Fatalf("Configure() error = %v, want ErrAuthorizationExists", err)
	}
	if readerCalled {
		t.Fatal("Configure() prompted for credentials despite an existing local session")
	}
	if session, ok := secretStore.value(tgaccount.SessionSecretAccount); !ok || string(session) != "session material" {
		t.Fatalf("Configure() changed the existing local session: %q, %t", session, ok)
	}
	if _, ok := secretStore.value(tgaccount.CredentialsSecretAccount); ok {
		t.Fatal("Configure() stored a new API hash despite an existing local session")
	}
}

func TestConfigureAcquiresLockBeforeReadingCredentials(t *testing.T) {
	t.Parallel()

	application, _ := newTestApplication(t)
	lock, err := daemon.AcquireAccountLock(application.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	readerCalled := false
	err = application.Configure(context.Background(), tgaccount.TestEnvironment, 2, func(context.Context) (int, []byte, error) {
		readerCalled = true
		return 12345, []byte(testAPIHash), nil
	})
	if !errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatalf("Configure() error = %v, want ErrAccountLocked", err)
	}
	if readerCalled {
		t.Fatal("Configure() read credentials before acquiring the account lock")
	}
}

func TestLogoutKeepsLocalStateWhenRemoteRevocationFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	application, secretStore := newTestApplication(t)
	if err := application.Configure(ctx, tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{authResult: tgaccount.AuthResult{Performed: true}}
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		return runtime, nil
	}
	if _, err := application.Authenticate(ctx, tgaccount.AuthMethodPhone, fakePrompt{}); err != nil {
		t.Fatal(err)
	}
	secretStore.set(tgaccount.SessionSecretAccount, []byte("session material"))
	runtime.logoutError = errors.New("remote revocation failed")
	if err := application.Logout(ctx); err == nil {
		t.Fatal("Logout() succeeded after remote revocation failed")
	}
	if _, ok := secretStore.value(tgaccount.SessionSecretAccount); !ok {
		t.Fatal("Logout() deleted the local session after remote revocation failed")
	}
	status, err := application.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authorized {
		t.Fatal("Logout() invalidated the authorization epoch after remote revocation failed")
	}
}

func TestDaemonAcquiresLockBeforeReadingSecrets(t *testing.T) {
	t.Parallel()

	application, secretStore := newTestApplication(t)
	if err := application.Configure(context.Background(), tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	lock, err := daemon.AcquireAccountLock(application.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	baseline := secretStore.readCount()
	if err := application.RunDaemon(context.Background()); !errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatalf("RunDaemon() error = %v, want ErrAccountLocked", err)
	}
	if got := secretStore.readCount(); got != baseline {
		t.Fatalf("secret reads = %d after lock failure, want %d", got, baseline)
	}
}

func TestDaemonOwnsLockSocketAndStopsOnCancellation(t *testing.T) {
	t.Parallel()

	application, _ := newTestApplication(t)
	if err := application.Configure(context.Background(), tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	recordDaemonAuthorization(t, application, 2)
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		return &fakeRuntime{observeStatus: tgaccount.AuthorizationStatus{Authorized: true}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.RunDaemon(ctx) }()
	waitForLifecycle(t, application, daemon.StateReady)
	if state, err := daemon.ProbeSocket(application.paths.Socket); err != nil || state != daemon.SocketLive {
		t.Fatalf("ProbeSocket() = %q, %v", state, err)
	}
	if lock, err := daemon.AcquireAccountLock(application.paths.Lock); lock != nil || !errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatalf("second AcquireAccountLock() = %v, %v", lock, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunDaemon() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunDaemon() did not stop after cancellation")
	}
	if got := application.Lifecycle().State; got != daemon.StateStopped {
		t.Fatalf("Lifecycle() state = %q", got)
	}
	if state, err := daemon.ProbeSocket(application.paths.Socket); err != nil || state != daemon.SocketAbsent {
		t.Fatalf("ProbeSocket() after stop = %q, %v", state, err)
	}
	lock, err := daemon.AcquireAccountLock(application.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonRequiresRecordedEpochBeforeOpeningTextRuntime(t *testing.T) {
	application, secrets := newTestApplication(t)
	if err := application.Configure(context.Background(), tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	baseline := secrets.readCount()
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		t.Error("constructed a text runtime without authorization metadata")
		return nil, errors.New("unexpected account construction")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.RunDaemon(ctx) }()
	waitForLifecycle(t, application, daemon.StateReauthRequired)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if secrets.readCount() != baseline {
		t.Fatal("daemon opened credentials without recorded authorization")
	}
	db, repository, err := application.openRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, exists, err := repository.Authorization(context.Background()); err != nil || exists {
		t.Fatal("daemon manufactured authorization metadata")
	}
}

func TestUnauthorizedDaemonInvalidatesStaleEpoch(t *testing.T) {
	t.Parallel()

	application, _ := newTestApplication(t)
	if err := application.Configure(context.Background(), tgaccount.TestEnvironment, 1, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	database, repository, err := application.openRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordAuthorization(
		context.Background(),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		nil,
		1,
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		return &fakeRuntime{observeStatus: tgaccount.AuthorizationStatus{}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.RunDaemon(ctx) }()
	waitForLifecycle(t, application, daemon.StateReauthRequired)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	status, err := application.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Authorized {
		t.Fatal("unauthorized Telegram session retained a stale epoch")
	}
}

func TestDaemonInvalidatesEpochWhenAuthorizationExpiresAfterReady(t *testing.T) {
	t.Parallel()

	application, _ := newTestApplication(t)
	if err := application.Configure(context.Background(), tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	database, repository, err := application.openRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordAuthorization(
		context.Background(),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		nil,
		2,
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	revoke := make(chan struct{})
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		return &fakeRuntime{
			observeStatus:  tgaccount.AuthorizationStatus{Authorized: true},
			observeFailure: revoke,
			observeError:   tgaccount.ErrReauthenticationRequired,
		}, nil
	}

	done := make(chan error, 1)
	go func() { done <- application.RunDaemon(context.Background()) }()
	waitForLifecycle(t, application, daemon.StateReady)
	close(revoke)
	select {
	case err := <-done:
		if !errors.Is(err, tgaccount.ErrReauthenticationRequired) {
			t.Fatalf("RunDaemon() error = %v, want ErrReauthenticationRequired", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunDaemon() did not stop after authorization revocation")
	}
	if got := application.Lifecycle().State; got != daemon.StateReauthRequired {
		t.Fatalf("Lifecycle() state = %q, want %q", got, daemon.StateReauthRequired)
	}
	status, err := application.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Authorized {
		t.Fatal("runtime authorization revocation retained the authorization epoch")
	}
}

func TestDaemonCleansUpAfterSIGTERM(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	runtimeRoot := filepath.Join(shortRuntimeRoot(t), "runtime")
	processContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(processContext, os.Args[0], "-test.run=^TestDaemonSIGTERMHelper$")
	command.Env = []string{
		signalHelperStateEnvironment + "=" + stateRoot,
		signalHelperRuntimeEnvironment + "=" + runtimeRoot,
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("daemon signal helper did not become ready: %q, %v", scanner.Text(), scanner.Err())
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.NewPaths(stateRoot, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := daemon.ProbeSocket(paths.Socket); err != nil || state != daemon.SocketAbsent {
		t.Fatalf("ProbeSocket() after SIGTERM = %q, %v", state, err)
	}
	lock, err := daemon.AcquireAccountLock(paths.Lock)
	if err != nil {
		t.Fatalf("account lock remained held after SIGTERM: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonSIGTERMHelper(t *testing.T) {
	stateRoot := os.Getenv(signalHelperStateEnvironment)
	runtimeRoot := os.Getenv(signalHelperRuntimeEnvironment)
	if stateRoot == "" || runtimeRoot == "" {
		return
	}
	paths, err := daemon.NewPaths(stateRoot, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecretStore{values: make(map[string][]byte)}
	application, err := New(paths, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Configure(context.Background(), tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	recordDaemonAuthorization(t, application, 2)
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		return &fakeRuntime{observeStatus: tgaccount.AuthorizationStatus{Authorized: true}}, nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- application.RunDaemon(ctx) }()
	waitForLifecycle(t, application, daemon.StateReady)
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func newTestApplication(t *testing.T) (*Application, *fakeSecretStore) {
	t.Helper()
	stateRoot := filepath.Join(t.TempDir(), "state")
	runtimeRoot := filepath.Join(shortRuntimeRoot(t), "runtime")
	paths, err := daemon.NewPaths(stateRoot, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecretStore{values: make(map[string][]byte)}
	application, err := New(paths, secrets)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC)
	application.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	application.random = bytes.NewReader(bytes.Repeat([]byte{0x42}, 1024))
	return application, secrets
}

func staticConfiguration(apiID int) ConfigurationReader {
	return func(context.Context) (int, []byte, error) {
		return apiID, []byte(testAPIHash), nil
	}
}

func shortRuntimeRoot(t *testing.T) string {
	t.Helper()
	temporaryRoot := "/tmp"
	if runtime.GOOS == "windows" {
		temporaryRoot = t.TempDir()
	}
	directory, err := os.MkdirTemp(temporaryRoot, "tmcp-app-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove runtime directory: %v", err)
		}
	})
	return directory
}

func authorizationState(t *testing.T, path string) metastore.AuthorizationState {
	t.Helper()
	database, err := metastore.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository, err := metastore.NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	state, exists, err := repository.Authorization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("authorization state does not exist")
	}
	return state
}

func waitForLifecycle(t *testing.T, application *Application, expected daemon.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if application.Lifecycle().State == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("lifecycle state = %q, want %q", application.Lifecycle().State, expected)
}

type fakeRuntime struct {
	authResult            tgaccount.AuthResult
	authError             error
	invalidatedBeforeAuth bool
	observeStatus         tgaccount.AuthorizationStatus
	observeFailure        <-chan struct{}
	observeError          error
	logoutError           error
}

func (f *fakeRuntime) EnableReads(context.Context, *sql.DB, string) error { return nil }
func (f *fakeRuntime) Ready() bool                                        { return f.observeStatus.Authorized }
func (f *fakeRuntime) SelfID() model.PeerID {
	id, _ := model.NewPeerID(model.PeerKindUser, 1)
	return id
}
func (f *fakeRuntime) Chat(context.Context, model.PeerID) (model.Chat, error) {
	return model.Chat{}, errors.New("fake metadata unavailable")
}
func (f *fakeRuntime) History(context.Context, model.HistoryQuery) ([]model.Candidate, error) {
	return nil, errors.New("fake history unavailable")
}
func (f *fakeRuntime) Acknowledge(context.Context, model.PeerID, int32) error {
	return errors.New("fake acknowledgment unavailable")
}

func recordDaemonAuthorization(t *testing.T, a *Application, dc int) {
	t.Helper()
	db, repository, err := a.openRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := repository.RecordAuthorization(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil, dc, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeRuntime) Observe(ctx context.Context, callback func(context.Context, tgaccount.AuthorizationStatus) error) error {
	if f.observeFailure == nil {
		return callback(ctx, f.observeStatus)
	}
	observeContext, cancel := context.WithCancel(ctx)
	defer cancel()
	callbackDone := make(chan error, 1)
	go func() { callbackDone <- callback(observeContext, f.observeStatus) }()
	select {
	case callbackError := <-callbackDone:
		return callbackError
	case <-f.observeFailure:
		cancel()
		callbackError := <-callbackDone
		if callbackError != nil && !errors.Is(callbackError, context.Canceled) {
			return callbackError
		}
		return f.observeError
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeRuntime) Authenticate(
	ctx context.Context,
	_ tgaccount.AuthMethod,
	_ tgaccount.Prompt,
	onUnauthorized func(context.Context) error,
) (tgaccount.AuthResult, error) {
	if f.authResult.Performed && onUnauthorized != nil {
		if err := onUnauthorized(ctx); err != nil {
			return tgaccount.AuthResult{}, err
		}
		f.invalidatedBeforeAuth = true
	}
	return f.authResult, f.authError
}

func (f *fakeRuntime) Logout(context.Context) error {
	return f.logoutError
}

type fakeSecretStore struct {
	mutex    sync.Mutex
	values   map[string][]byte
	getCount int
}

func (f *fakeSecretStore) Put(_ context.Context, account string, value []byte) error {
	f.set(account, value)
	return nil
}

func (f *fakeSecretStore) Get(_ context.Context, account string) ([]byte, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.getCount++
	value, ok := f.values[account]
	if !ok {
		return nil, keychain.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (f *fakeSecretStore) readCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.getCount
}

func (f *fakeSecretStore) Delete(_ context.Context, account string) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	delete(f.values, account)
	return nil
}

func (f *fakeSecretStore) set(account string, value []byte) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.values[account] = append([]byte(nil), value...)
}

func (f *fakeSecretStore) value(account string) ([]byte, bool) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	value, ok := f.values[account]
	return append([]byte(nil), value...), ok
}

type fakePrompt struct{}

func (fakePrompt) Phone(context.Context) ([]byte, error) { return []byte("9996621234"), nil }
func (fakePrompt) Code(context.Context) ([]byte, error)  { return []byte("22222"), nil }
func (fakePrompt) Password(context.Context) ([]byte, error) {
	return []byte("password"), nil
}
func (fakePrompt) ShowQRCode(context.Context, string, time.Time) error { return nil }

func TestInterruptedConfigurationCannotConstructMixedAccount(t *testing.T) {
	for _, cancellation := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancellation=%t", cancellation), func(t *testing.T) {
			application, secrets := newTestApplication(t)
			ctx := context.Background()
			if err := application.Configure(ctx, tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
				t.Fatal(err)
			}
			database, repository, err := application.openRepository(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			writeContext, cancel := context.WithCancel(ctx)
			defer cancel()
			if cancellation {
				application.secrets = &cancelAfterCredentialWrite{fakeSecretStore: secrets, cancel: cancel}
			} else {
				if _, err := database.ExecContext(ctx, `CREATE TRIGGER reject_config BEFORE UPDATE ON account_config BEGIN SELECT RAISE(ABORT, 'write rejected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err = application.Configure(writeContext, tgaccount.TestEnvironment, 3, staticConfiguration(54321))
			if err == nil {
				t.Fatal("expected interrupted configuration")
			}
			if cancellation && !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
			configuration, exists, err := repository.Config(ctx)
			if err != nil || !exists || configuration.APIID != 12345 || configuration.TestDC != 2 {
				t.Fatal("old metadata was not preserved")
			}
			stored, err := tgaccount.LoadCredentials(ctx, secrets)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(stored.APIHash)
			if stored.APIID != 54321 || stored.TestDC != 3 {
				t.Fatal("Secret store did not atomically retain new credential tuple")
			}
			restarted, err := New(application.paths, secrets)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			restarted.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
				called = true
				return &fakeRuntime{}, nil
			}
			if _, _, err := restarted.account(ctx, repository, tgaccount.ModeNoUpdates); !errors.Is(err, ErrConfigurationRequired) || called {
				t.Fatalf("mixed configuration reached runtime: called=%t error=%v", called, err)
			}
			if !cancellation {
				if _, err := database.ExecContext(ctx, "DROP TRIGGER reject_config"); err != nil {
					t.Fatal(err)
				}
			}
			if err := restarted.Configure(ctx, tgaccount.TestEnvironment, 3, staticConfiguration(54321)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := restarted.account(ctx, repository, tgaccount.ModeNoUpdates); err != nil || !called {
				t.Fatalf("reconfiguration failed to recover: %v", err)
			}
		})
	}
}

type cancelAfterCredentialWrite struct {
	*fakeSecretStore
	cancel context.CancelFunc
}

func (s *cancelAfterCredentialWrite) Put(ctx context.Context, account string, value []byte) error {
	if err := s.fakeSecretStore.Put(ctx, account, value); err != nil {
		return err
	}
	s.cancel()
	return nil
}

func TestUnconfiguredDaemonServesStaticToolsWithoutReadingSecrets(t *testing.T) {
	application, secrets := newTestApplication(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.RunDaemon(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("daemon did not stop")
		}
	}()
	waitForLifecycle(t, application, daemon.StateReauthRequired)
	deadline := time.Now().Add(2 * time.Second)
	var connection net.Conn
	var err error
	for time.Now().Before(deadline) {
		connection, err = daemon.DialSocket(ctx, application.paths.Socket)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "account-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: connection, Writer: connection}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 9 {
		t.Fatalf("tools=%v error=%v", tools, err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status"}); err != nil {
		t.Fatal(err)
	}
	if secrets.readCount() != 0 {
		t.Fatal("unconfigured status read secrets")
	}
}

func (f *fakeRuntime) Search(ctx context.Context, q model.SearchQuery) ([]model.Candidate, error) {
	return f.History(ctx, model.HistoryQuery{Peer: q.Peer, Before: q.Before, MinID: q.MinID, MaxID: q.MaxID, Limit: q.Limit})
}
func (f *fakeRuntime) Unread(_ context.Context, peer model.PeerID) (model.Unread, error) {
	return model.Unread{Peer: peer, Count: 1}, nil
}
