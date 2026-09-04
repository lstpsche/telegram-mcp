package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var (
	ErrSocketInUse  = errors.New("the runtime socket is already active")
	ErrUnsafeSocket = errors.New("the runtime socket path is unsafe")
)

const socketProbeTimeout = 200 * time.Millisecond

type socketDialer func(network, address string, timeout time.Duration) (net.Conn, error)

type fileIdentity struct {
	device uint64
	inode  uint64
}

// Socket owns an owner-only Unix-domain listener and its exact filesystem node.
type Socket struct {
	listener *net.UnixListener
	path     string
	identity fileIdentity
	once     sync.Once
	err      error
}

type SocketState string

const (
	SocketAbsent SocketState = "absent"
	SocketLive   SocketState = "live"
	SocketStale  SocketState = "stale"
)

// BindSocket rejects unresolved or unsafe existing paths, removes only a
// verified same-user stale socket, and binds an owner-only listener.
func BindSocket(path string) (*Socket, error) {
	return bindSocket(path, net.DialTimeout)
}

func bindSocket(path string, dial socketDialer) (*Socket, error) {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	var rawAddress unix.RawSockaddrUnix
	if len(path) >= len(rawAddress.Path) {
		return nil, errors.New("runtime socket path exceeds the platform limit")
	}

	if identity, err := inspectSocket(path); err == nil {
		connection, dialError := dial("unix", path, socketProbeTimeout)
		if dialError == nil {
			_ = connection.Close()
			return nil, ErrSocketInUse
		}
		if networkError, ok := dialError.(net.Error); ok && networkError.Timeout() {
			return nil, ErrSocketInUse
		}
		if !errors.Is(dialError, unix.ECONNREFUSED) {
			return nil, ErrSocketInUse
		}
		currentIdentity, inspectError := inspectSocket(path)
		if inspectError != nil || currentIdentity != identity {
			return nil, ErrUnsafeSocket
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove verified stale runtime socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("bind runtime socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	closeOnError := func(openError error) (*Socket, error) {
		closeError := listener.Close()
		removeError := os.Remove(path)
		if errors.Is(removeError, os.ErrNotExist) {
			removeError = nil
		}
		return nil, errors.Join(openError, closeError, removeError)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return closeOnError(fmt.Errorf("secure runtime socket: %w", err))
	}
	identity, err := inspectSocket(path)
	if err != nil {
		return closeOnError(err)
	}
	return &Socket{listener: listener, path: path, identity: identity}, nil
}

// ServeProbes accepts and immediately closes liveness probes. Phase 4 replaces
// this narrow loop with the bounded MCP connection handler.
func (s *Socket) ServeProbes(ctx context.Context) error {
	if s == nil || s.listener == nil {
		return errors.New("runtime socket is not initialized")
	}
	for {
		if err := s.listener.SetDeadline(time.Now().Add(socketProbeTimeout)); err != nil {
			return fmt.Errorf("set runtime socket deadline: %w", err)
		}
		connection, err := s.listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
				continue
			}
			return fmt.Errorf("accept runtime socket probe: %w", err)
		}
		if err := connection.Close(); err != nil {
			return fmt.Errorf("close runtime socket probe: %w", err)
		}
	}
}

// Close closes the listener and removes only the socket node originally bound.
func (s *Socket) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.listener != nil {
			s.err = s.listener.Close()
		}
		identity, err := inspectSocket(s.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			s.err = errors.Join(s.err, err)
		case identity != s.identity:
			s.err = errors.Join(s.err, ErrUnsafeSocket)
		default:
			s.err = errors.Join(s.err, os.Remove(s.path))
		}
	})
	return s.err
}

// ProbeSocket inspects the daemon socket without changing it.
func ProbeSocket(path string) (SocketState, error) {
	return probeSocket(path, net.DialTimeout)
}

func probeSocket(path string, dial socketDialer) (SocketState, error) {
	if err := inspectPrivateDirectory(filepath.Dir(path)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SocketAbsent, nil
		}
		return "", err
	}
	if _, err := inspectSocket(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SocketAbsent, nil
		}
		return "", err
	}
	connection, err := dial("unix", path, socketProbeTimeout)
	if err == nil {
		_ = connection.Close()
		return SocketLive, nil
	}
	if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
		return SocketLive, nil
	}
	if errors.Is(err, unix.ECONNREFUSED) {
		return SocketStale, nil
	}
	return "", ErrSocketInUse
}

func inspectSocket(path string) (fileIdentity, error) {
	var status unix.Stat_t
	if err := unix.Lstat(path, &status); err != nil {
		return fileIdentity{}, err
	}
	if status.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fileIdentity{}, ErrUnsafeSocket
	}
	if status.Uid != uint32(os.Geteuid()) {
		return fileIdentity{}, ErrUnsafeSocket
	}
	if os.FileMode(status.Mode).Perm() != 0o600 {
		return fileIdentity{}, ErrUnsafeSocket
	}
	return fileIdentity{device: uint64(status.Dev), inode: uint64(status.Ino)}, nil
}
