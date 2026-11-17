//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSocketPublicationExposesOnlySecuredListener(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "stale"}[stale], func(t *testing.T) {
			path := filepath.Join(shortTempDir(t), socketFilename)
			if stale {
				staleUnixSocket(t, path)
			}
			var temporary string
			publisher := func(source, target string) error {
				temporary = source
				if filepath.Dir(source) != filepath.Dir(target) {
					t.Fatal("staging used a different directory")
				}
				if _, err := inspectSocket(source); err != nil {
					t.Fatal("staging inode was not secured", err)
				}
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("final path visible before publication", err)
				}
				if conn, err := DialSocket(context.Background(), target); conn != nil || !errors.Is(err, ErrDaemonUnavailable) {
					t.Fatal("unpublished endpoint was not positively absent", err)
				}
				return os.Link(source, target)
			}
			socket, err := bindSocketWithPublisher(path, net.DialTimeout, publisher)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("staging alias remains", err)
			}
			identity, err := inspectSocket(path)
			if err != nil || identity != socket.identity {
				t.Fatal("published identity changed", err)
			}
			connection, err := DialSocket(context.Background(), path)
			if err != nil {
				t.Fatal("published hardlink did not connect", err)
			}
			defer connection.Close()
			accepted, err := socket.listener.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer accepted.Close()
			done := make(chan error, 1)
			go func() { _, err := connection.Write([]byte("ping")); done <- err }()
			var data [4]byte
			if _, err := io.ReadFull(accepted, data[:]); err != nil || string(data[:]) != "ping" {
				t.Fatal("published listener did not receive bytes", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := socket.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("published alias remains", err)
			}
		})
	}
}

func TestSocketPublicationNeverReplacesConcurrentDestination(t *testing.T) {
	path := filepath.Join(shortTempDir(t), socketFilename)
	var temporary string
	publisher := func(source, target string) error {
		temporary = source
		if err := os.WriteFile(target, []byte("unrelated"), 0600); err != nil {
			t.Fatal(err)
		}
		return os.Link(source, target)
	}
	if socket, err := bindSocketWithPublisher(path, net.DialTimeout, publisher); socket != nil || err == nil {
		t.Fatal("concurrent destination was replaced", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "unrelated" {
		t.Fatal("concurrent destination changed", err)
	}
	if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed publication retained staging alias", err)
	}
}

func TestSocketPublicationFailureKeepsReplacedAliases(t *testing.T) {
	for _, replace := range []string{"temporary", "published"} {
		t.Run(replace, func(t *testing.T) {
			path := filepath.Join(shortTempDir(t), socketFilename)
			var replaced string
			publisher := func(source, target string) error {
				replaced = source
				if replace == "published" {
					if err := os.Link(source, target); err != nil {
						t.Fatal(err)
					}
					replaced = target
				}
				if err := os.Rename(replaced, replaced+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(replaced, []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
				if replace == "temporary" {
					return errors.New("synthetic publication failure")
				}
				return nil
			}
			if socket, err := bindSocketWithPublisher(path, net.DialTimeout, publisher); socket != nil || !errors.Is(err, ErrUnsafeSocket) {
				t.Fatal("alias replacement was not rejected", err)
			}
			if data, err := os.ReadFile(replaced); err != nil || string(data) != "unrelated" {
				t.Fatal("failure cleanup removed a replaced alias", err)
			}
		})
	}
}

func TestSocketPublicationPreservesMaximumPathLength(t *testing.T) {
	root := shortTempDir(t)
	var address unix.RawSockaddrUnix
	parentLength := len(address.Path) - 3
	directory := filepath.Join(root, strings.Repeat("d", parentLength-len(root)-1))
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "x")
	if len(path) != len(address.Path)-1 {
		t.Fatal("test path does not reach the existing limit")
	}
	socket, err := BindSocket(path)
	if err != nil {
		t.Fatal("valid maximum-length socket path was rejected", err)
	}
	defer socket.Close()
	connection, err := DialSocket(context.Background(), path)
	if err != nil {
		t.Fatal("maximum-length published socket did not connect", err)
	}
	connection.Close()
}
