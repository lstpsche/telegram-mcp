package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"golang.org/x/sys/windows"
)

var (
	ErrSocketInUse  = errors.New("the runtime socket is already active")
	ErrUnsafeSocket = errors.New("the runtime socket path is unsafe")
)

const socketProbeTimeout = 200 * time.Millisecond

type SocketState string

const (
	SocketAbsent SocketState = "absent"
	SocketLive   SocketState = "live"
	SocketStale  SocketState = "stale"
)

type Socket struct {
	listener net.Listener
	once     sync.Once
	err      error
}

// Pipe names are fixed-length, account-specific, and scoped to the runtime path.
func pipeName(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrUnsafeSocket
	}
	sid, err := privatefs.CurrentUserSID()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(sid.String() + "\x00" + strings.ToLower(filepath.Clean(path))))
	return `\\.\pipe\telegram-mcp-` + hex.EncodeToString(digest[:]), nil
}
func BindSocket(path string) (*Socket, error) {
	if err := privatefs.EnsureDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	name, err := pipeName(path)
	if err != nil {
		return nil, err
	}
	descriptor, err := privatefs.SecurityDescriptor()
	if err != nil {
		return nil, err
	}
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: descriptor, MessageMode: true, InputBufferSize: 65536, OutputBufferSize: 65536})
	if err != nil {
		return nil, err
	}
	return &Socket{listener: listener}, nil
}
func DialSocket(ctx context.Context, path string) (net.Conn, error) {
	if err := privatefs.CheckDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	name, err := pipeName(path)
	if err != nil {
		return nil, err
	}
	dialContext, cancel := context.WithTimeout(ctx, socketProbeTimeout)
	defer cancel()
	conn, err := winio.DialPipeContext(dialContext, name)
	if err != nil {
		return nil, err
	}
	if err = verifyPipeServer(conn); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return conn, nil
}
func (s *Socket) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.listener != nil {
			s.err = s.listener.Close()
		}
	})
	return s.err
}
func ProbeSocket(path string) (SocketState, error) {
	conn, err := DialSocket(context.Background(), path)
	if err == nil {
		return SocketLive, conn.Close()
	}
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return SocketAbsent, nil
	}
	return "", err
}

func pipeProcess(connection net.Conn, server bool) (result error) {
	handle, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return ErrUnsafeSocket
	}
	var pid uint32
	var err error
	if server {
		err = windows.GetNamedPipeServerProcessId(windows.Handle(handle.Fd()), &pid)
	} else {
		err = windows.GetNamedPipeClientProcessId(windows.Handle(handle.Fd()), &pid)
	}
	if err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(process)) }()
	var token windows.Token
	if err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	defer func() { result = errors.Join(result, token.Close()) }()
	user, err := token.GetTokenUser()
	if err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	sid, err := privatefs.CurrentUserSID()
	if err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	if !sid.Equals(user.User.Sid) {
		return ErrUnsafeSocket
	}
	return nil
}
func VerifyPeer(connection net.Conn) error       { return pipeProcess(connection, false) }
func verifyPipeServer(connection net.Conn) error { return pipeProcess(connection, true) }
