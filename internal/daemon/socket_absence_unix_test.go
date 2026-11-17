//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDialSocketClassifiesOnlySafeAbsence(t *testing.T) {
	for _, scenario := range []string{"missing directory", "missing socket", "refused socket", "unsafe ancestor", "broken ancestor link", "unsafe directory", "socket link", "regular file", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			root := shortTempDir(t)
			directory := filepath.Join(root, "runtime")
			path := filepath.Join(directory, socketFilename)
			ctx := context.Background()
			wantUnavailable := false
			switch scenario {
			case "missing directory":
				wantUnavailable = true
			case "unsafe ancestor":
				if err := os.Chmod(root, 0777); err != nil {
					t.Fatal(err)
				}
			case "broken ancestor link":
				if err := os.Symlink(filepath.Join(root, "absent"), directory); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(directory, "nested", socketFilename)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			default:
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "missing socket":
					wantUnavailable = true
				case "refused socket":
					staleUnixSocket(t, path)
					wantUnavailable = true
				case "unsafe directory":
					if err := os.Chmod(directory, 0755); err != nil {
						t.Fatal(err)
					}
				case "socket link":
					if err := os.Symlink(filepath.Join(root, "absent"), path); err != nil {
						t.Fatal(err)
					}
				case "regular file":
					if err := os.WriteFile(path, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			connection, err := DialSocket(ctx, path)
			if connection != nil || err == nil || errors.Is(err, ErrDaemonUnavailable) != wantUnavailable {
				t.Fatalf("connection=%v error=%v unavailable=%v, want %v", connection, err, errors.Is(err, ErrDaemonUnavailable), wantUnavailable)
			}
			if scenario == "missing directory" {
				if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("dial created state: %v", err)
				}
			}
			if scenario == "refused socket" {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("dial removed stale socket: %v", err)
				}
			}
			if scenario == "unsafe ancestor" || scenario == "broken ancestor link" || scenario == "unsafe directory" {
				if state, err := ProbeSocket(path); state != "" || err == nil {
					t.Fatalf("unsafe probe = %q, %v", state, err)
				}
			}
		})
	}
}

func staleUnixSocket(t *testing.T, path string) {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDialSocketRefusalRequiresUnchangedIdentity(t *testing.T) {
	for _, replace := range []string{"file", "socket", "unsafe directory", "inconclusive", "joined unsafe", "joined cancellation"} {
		t.Run(replace, func(t *testing.T) {
			path := filepath.Join(shortTempDir(t), socketFilename)
			staleUnixSocket(t, path)
			dial := func(context.Context, string, string) (net.Conn, error) {
				switch replace {
				case "file", "socket":
					if err := os.Rename(path, path+".old"); err != nil {
						t.Fatal(err)
					}
					if replace == "file" {
						if err := os.WriteFile(path, nil, 0600); err != nil {
							t.Fatal(err)
						}
					} else if replace == "socket" {
						staleUnixSocket(t, path)
					}
				case "unsafe directory":
					if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
				case "inconclusive":
					return nil, unix.EACCES
				case "joined unsafe":
					return nil, errors.Join(ErrUnsafeSocket, unix.ECONNREFUSED)
				case "joined cancellation":
					return nil, errors.Join(context.Canceled, unix.ECONNREFUSED)
				}
				return nil, unix.ECONNREFUSED
			}
			if conn, err := dialSocket(context.Background(), path, dial); conn != nil || err == nil || errors.Is(err, ErrDaemonUnavailable) {
				t.Fatalf("unsafe/inconclusive refusal became eligible: %v, %v", conn, err)
			}
		})
	}
}

func TestDialSocketDisappearanceRequiresFreshSafeAbsence(t *testing.T) {
	for _, cause := range []error{unix.ENOENT, unix.ECONNREFUSED} {
		for _, replacement := range []string{"absent", "existing", "file", "socket", "unsafe directory", "joined unsafe", "joined cancellation"} {
			t.Run(cause.Error()+"/"+replacement, func(t *testing.T) {
				path := filepath.Join(shortTempDir(t), socketFilename)
				staleUnixSocket(t, path)
				dial := func(context.Context, string, string) (net.Conn, error) {
					if replacement != "existing" {
						if err := os.Rename(path, path+".original"); err != nil {
							t.Fatal(err)
						}
					}
					switch replacement {
					case "file":
						if err := os.WriteFile(path, nil, 0600); err != nil {
							t.Fatal(err)
						}
					case "socket":
						staleUnixSocket(t, path)
					case "unsafe directory":
						if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
							t.Fatal(err)
						}
					case "joined unsafe":
						return nil, errors.Join(ErrUnsafeSocket, cause)
					case "joined cancellation":
						return nil, errors.Join(context.Canceled, cause)
					}
					return nil, cause
				}
				conn, err := dialSocket(context.Background(), path, dial)
				wantUnavailable := replacement == "absent" || replacement == "existing" && cause == unix.ECONNREFUSED
				if conn != nil || err == nil || errors.Is(err, ErrDaemonUnavailable) != wantUnavailable {
					t.Fatalf("connection=%v error=%v unavailable=%v want=%v", conn, err, errors.Is(err, ErrDaemonUnavailable), wantUnavailable)
				}
			})
		}
	}
}

func TestDialSocketTimeoutDoesNotAuthorizeStartup(t *testing.T) {
	path := filepath.Join(shortTempDir(t), socketFilename)
	staleUnixSocket(t, path)
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "unix", Err: os.ErrDeadlineExceeded}
	}
	_, err := dialSocket(context.Background(), path, dial)
	if err == nil || errors.Is(err, ErrDaemonUnavailable) {
		t.Fatalf("timeout classification: %v", err)
	}
	state, err := probeSocket(path, func(string, string, time.Duration) (net.Conn, error) { return dial(context.Background(), "", "") })
	if state != SocketLive || err != nil {
		t.Fatalf("probe timeout = %q, %v", state, err)
	}
}

func TestDialSocketConnectedIdentityReplacementRemainsFatal(t *testing.T) {
	path := filepath.Join(shortTempDir(t), socketFilename)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	var connected net.Conn
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		var err error
		connected, err = (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
		staleUnixSocket(t, path)
		return connected, nil
	}
	connection, err := dialSocket(context.Background(), path, dial)
	if connection != nil || !errors.Is(err, ErrUnsafeSocket) || errors.Is(err, ErrDaemonUnavailable) {
		t.Fatalf("replaced connected socket was not rejected: connection=%v error=%v", connection, err)
	}
	if _, err := connected.Write([]byte("closed")); err == nil {
		t.Fatal("rejected connection was retained")
	}
}
