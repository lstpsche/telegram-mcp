//go:build darwin || linux

package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	return bindSocketWithPublisher(path, dial, os.Link)
}

func bindSocketWithPublisher(path string, dial socketDialer, publish func(string, string) error) (*Socket, error) {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	var rawAddress unix.RawSockaddrUnix
	if len(path) >= len(rawAddress.Path) {
		return nil, errors.New("runtime socket path exceeds the platform limit")
	}

	var stale *fileIdentity
	if identity, err := inspectSocket(path); err == nil {
		connection, dialError := dial("unix", path, socketProbeTimeout)
		if dialError == nil {
			_ = connection.Close()
			return nil, ErrSocketInUse
		}
		if networkError, ok := dialError.(net.Error); ok && networkError.Timeout() {
			return nil, ErrSocketInUse
		}
		if !definiteDialFailure(dialError, unix.ECONNREFUSED) {
			return nil, ErrSocketInUse
		}
		currentIdentity, inspectError := inspectSocket(path)
		if inspectError != nil || currentIdentity != identity {
			return nil, ErrUnsafeSocket
		}
		stale = &identity
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	listener, temporary, identity, err := prepareSocket(path, len(rawAddress.Path))
	if err != nil {
		return nil, err
	}
	published := false
	closeOnError := func(openError error) (*Socket, error) {
		cleanupError := errors.Join(listener.Close(), removeSocketAlias(temporary, identity))
		if published {
			cleanupError = errors.Join(cleanupError, removeSocketAlias(path, identity))
		}
		return nil, errors.Join(openError, cleanupError)
	}
	if err := inspectPrivateDirectory(filepath.Dir(path)); err != nil {
		return closeOnError(err)
	}
	if stale != nil {
		current, err := inspectSocket(path)
		if err != nil || current != *stale {
			return closeOnError(errors.Join(ErrUnsafeSocket, err))
		}
		if err := os.Remove(path); err != nil {
			return closeOnError(fmt.Errorf("remove verified stale runtime socket: %w", err))
		}
	}
	// Link publishes the already secured inode atomically and never replaces an
	// existing destination. Both names stay in the same private directory.
	if err := publish(temporary, path); err != nil {
		return closeOnError(fmt.Errorf("publish runtime socket: %w", err))
	}
	published = true
	current, err := inspectSocket(path)
	if err != nil || current != identity {
		return closeOnError(errors.Join(ErrUnsafeSocket, err))
	}
	if err := removeSocketAlias(temporary, identity); err != nil {
		return closeOnError(err)
	}
	return &Socket{listener: listener, path: path, identity: identity}, nil
}

func prepareSocket(path string, addressSize int) (*net.UnixListener, string, fileIdentity, error) {
	directory := filepath.Dir(path)
	// Preserve the final path's address-length support, even when only one
	// basename byte fits. The private directory, not nonce length, is authority.
	nameSize := min(16, addressSize-len(directory)-2)
	if nameSize <= 0 {
		return nil, "", fileIdentity{}, errors.New("runtime socket path exceeds the platform limit")
	}
	for range 100 {
		temporary := filepath.Join(directory, rand.Text()[:nameSize])
		if strings.EqualFold(temporary, path) {
			continue
		}
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: temporary, Net: "unix"})
		if errors.Is(err, unix.EADDRINUSE) {
			continue
		}
		if err != nil {
			return nil, "", fileIdentity{}, fmt.Errorf("bind temporary runtime socket: %w", err)
		}
		listener.SetUnlinkOnClose(false)
		status, err := inspectSocketNode(temporary)
		if err != nil {
			return nil, "", fileIdentity{}, errors.Join(err, listener.Close())
		}
		identity := fileIdentity{device: uint64(status.Dev), inode: uint64(status.Ino)}
		if err := os.Chmod(temporary, 0o600); err != nil {
			return nil, "", fileIdentity{}, errors.Join(err, listener.Close(), removeSocketAlias(temporary, identity))
		}
		current, err := inspectSocket(temporary)
		if err != nil || current != identity {
			return nil, "", fileIdentity{}, errors.Join(ErrUnsafeSocket, err, listener.Close(), removeSocketAlias(temporary, identity))
		}
		return listener, temporary, identity, nil
	}
	return nil, "", fileIdentity{}, errors.New("cannot allocate temporary runtime socket")
}

func removeSocketAlias(path string, identity fileIdentity) error {
	status, err := inspectSocketNode(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if (fileIdentity{device: uint64(status.Dev), inode: uint64(status.Ino)}) != identity {
		return ErrUnsafeSocket
	}
	return os.Remove(path)
}

// DialSocket validates the directory, socket node, and server UID. It never
// creates state or starts a daemon on behalf of a client.
func DialSocket(ctx context.Context, path string) (net.Conn, error) {
	return dialSocket(ctx, path, (&net.Dialer{Timeout: socketProbeTimeout}).DialContext)
}

func dialSocket(ctx context.Context, path string, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := inspectDialDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	identity, err := inspectSocket(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, absentEndpoint(path, err)
		}
		return nil, err
	}
	connection, err := dial(ctx, "unix", path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if definiteDialFailure(err, unix.ECONNREFUSED) {
			current, inspectError := inspectSocket(path)
			if definiteDialFailure(inspectError, os.ErrNotExist) {
				return nil, confirmSocketAbsence(path, err)
			}
			if inspectError != nil || current != identity {
				return nil, errors.Join(ErrUnsafeSocket, inspectError)
			}
			return nil, absentEndpoint(path, err)
		}
		if definiteDialFailure(err, os.ErrNotExist) {
			return nil, confirmSocketAbsence(path, err)
		}
		return nil, err
	}
	current, err := inspectSocket(path)
	if err != nil || current != identity {
		return nil, errors.Join(ErrUnsafeSocket, err, connection.Close())
	}
	if err := VerifyPeer(connection); err != nil {
		return nil, errors.Join(ErrUnsafeSocket, err, connection.Close())
	}
	return connection, nil
}

func confirmSocketAbsence(path string, cause error) error {
	if err := inspectDialDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	_, err := inspectSocket(path)
	if definiteDialFailure(err, os.ErrNotExist) {
		return errors.Join(ErrDaemonUnavailable, cause)
	}
	return errors.Join(ErrUnsafeSocket, err)
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
	if err := inspectDialDirectory(filepath.Dir(path)); err != nil {
		if errors.Is(err, ErrDaemonUnavailable) {
			return SocketAbsent, nil
		}
		return "", err
	}
	identity, err := inspectSocket(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = absentEndpoint(path, err)
			if errors.Is(err, ErrDaemonUnavailable) {
				return SocketAbsent, nil
			}
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
	if definiteDialFailure(err, unix.ECONNREFUSED) {
		current, inspectError := inspectSocket(path)
		if definiteDialFailure(inspectError, os.ErrNotExist) {
			err = confirmSocketAbsence(path, err)
			if errors.Is(err, ErrDaemonUnavailable) {
				return SocketAbsent, nil
			}
			return "", err
		}
		if inspectError != nil || current != identity {
			return "", errors.Join(ErrUnsafeSocket, inspectError)
		}
		if err := inspectDialDirectory(filepath.Dir(path)); err != nil {
			return "", err
		}
		return SocketStale, nil
	}
	if definiteDialFailure(err, os.ErrNotExist) {
		err = confirmSocketAbsence(path, err)
		if errors.Is(err, ErrDaemonUnavailable) {
			return SocketAbsent, nil
		}
		return "", err
	}
	return "", ErrSocketInUse
}

func inspectSocket(path string) (fileIdentity, error) {
	status, err := inspectSocketNode(path)
	if err != nil {
		return fileIdentity{}, err
	}
	if os.FileMode(status.Mode).Perm() != 0o600 {
		return fileIdentity{}, ErrUnsafeSocket
	}
	return fileIdentity{device: uint64(status.Dev), inode: uint64(status.Ino)}, nil
}

func inspectSocketNode(path string) (unix.Stat_t, error) {
	var status unix.Stat_t
	if err := unix.Lstat(path, &status); err != nil {
		return status, err
	}
	if status.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return status, ErrUnsafeSocket
	}
	if status.Uid != uint32(os.Geteuid()) {
		return status, ErrUnsafeSocket
	}
	return status, nil
}
