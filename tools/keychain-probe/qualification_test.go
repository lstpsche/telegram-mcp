//go:build darwin

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/keychaincheck"
)

func TestQualificationExercisesEveryRequiredPair(t *testing.T) {
	artifacts := []artifact{{path: "control"}, {path: "daemon"}, {path: "up-control"}, {path: "up-daemon"}}
	states := map[string]string{}
	calls := 0
	var trace []string
	run := func(ctx context.Context, path, action, token string) (keychaincheck.Result, error) {
		calls++
		trace = append(trace, path+":"+action)
		switch action {
		case "create":
			if states[token] != "" {
				t.Fatal("token reused")
			}
			states[token] = "initial"
		case "read":
			if states[token] != "initial" {
				t.Fatal("read wrong value")
			}
		case "update":
			if states[token] != "initial" {
				t.Fatal("update wrong value")
			}
			states[token] = "replacement"
		case "verify":
			if states[token] != "replacement" {
				t.Fatal("verify wrong value")
			}
		case "delete":
			delete(states, token)
		case "absent":
			if states[token] != "" {
				t.Fatal("item remains")
			}
		default:
			t.Fatal("unexpected action")
		}
		return keychaincheck.Result{OK: true, Category: "ok"}, nil
	}
	report, err := qualify(context.Background(), artifacts, run)
	if err != nil || !report.OK || len(report.Checks) != 8 || calls != 48 || len(states) != 0 {
		t.Fatalf("report=%+v calls=%d err=%v", report, calls, err)
	}
	for _, check := range report.Checks {
		if !check.OK || check.Cleanup != "verified_absent" {
			t.Fatal("unverified outcome")
		}
	}
	for i, pair := range [][2]string{{"control", "control"}, {"daemon", "daemon"}, {"control", "daemon"}, {"daemon", "control"}, {"control", "up-control"}, {"daemon", "up-daemon"}, {"up-control", "up-daemon"}, {"up-daemon", "up-control"}} {
		want := []string{pair[0] + ":create", pair[1] + ":read", pair[1] + ":update", pair[0] + ":verify", pair[0] + ":delete", pair[0] + ":absent"}
		if strings.Join(trace[i*6:(i+1)*6], ",") != strings.Join(want, ",") {
			t.Fatalf("wrong artifact pair: %v", trace[i*6:(i+1)*6])
		}
	}
}

func TestQualificationFailureCleansThroughCreator(t *testing.T) {
	for _, failure := range []string{"denied", "process", "timeout", "cancelled", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var actions []string
			run := func(ctx context.Context, path, action, token string) (keychaincheck.Result, error) {
				actions = append(actions, path+":"+action)
				if action == "read" {
					if failure == "timeout" {
						return keychaincheck.Result{}, context.DeadlineExceeded
					}
					if failure == "cancelled" {
						cancel()
						return keychaincheck.Result{}, context.Canceled
					}
					if failure == "process" {
						return keychaincheck.Result{}, errors.New("raw child detail")
					}
					return keychaincheck.Result{Category: "interaction_unavailable", NativeStatus: -25308}, nil
				}
				if action == "delete" || action == "absent" {
					if ctx.Err() != nil {
						t.Fatal("cleanup inherited cancellation")
					}
					if failure == "cleanup" {
						return keychaincheck.Result{}, errors.New("cleanup unavailable")
					}
				}
				return keychaincheck.Result{OK: true, Category: "ok"}, nil
			}
			result, err := exercise(ctx, "sharing", "creator", "reader", "token", run)
			if err == nil || result.OK || result.FailedAction != "read" {
				t.Fatalf("false success %+v %v", result, err)
			}
			if failure == "timeout" && result.Category != "worker_timeout" {
				t.Fatal("timeout misreported as native denial")
			}
			if failure == "cleanup" {
				if result.Cleanup != "failed" {
					t.Fatal("cleanup failure hidden")
				}
			} else if result.Cleanup != "verified_absent" || strings.Join(actions, ",") != "creator:create,reader:read,creator:delete,creator:absent" {
				t.Fatalf("wrong cleanup %+v %v", result, actions)
			}
		})
	}
}

func TestCollisionDoesNotDeletePreexistingItem(t *testing.T) {
	calls := 0
	result, err := exercise(context.Background(), "collision", "creator", "reader", "token", func(context.Context, string, string, string) (keychaincheck.Result, error) {
		calls++
		return keychaincheck.Result{Category: "already_exists"}, nil
	})
	if err == nil || result.OK || calls != 1 || result.Cleanup != "not_created" {
		t.Fatal("collision touched existing item")
	}
}

func TestWorkerProtocolRejectsHostileOutput(t *testing.T) {
	for _, body := range []string{
		`printf '%s\n' '{"ok":true,"category":"ok"}'; exit 1`,
		`printf '%s\n' '{"ok":false,"category":"private text"}'; exit 1`,
		`printf '%s\n' '{"ok":true,"category":"ok","secret":"private"}'`,
		`printf '%s\n' '{"ok":true,"category":"ok"}' 'extra'`,
		`printf '%s\n' 'private error' >&2; exit 1`,
	} {
		path := filepath.Join(t.TempDir(), "worker")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := invokeWorker(context.Background(), path, "read", "token"); err == nil {
			t.Fatal("invalid worker response accepted")
		}
	}
	var copied boundedOutput
	// Hide WriterTo so io.Copy would use a promoted bytes.Buffer.ReadFrom.
	reader := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 4096))}
	if _, err := io.Copy(&copied, reader); err == nil || copied.Len() > 2048 {
		t.Fatal("io.Copy bypassed output limit")
	}
	var out boundedOutput
	if _, err := out.Write(make([]byte, 2049)); err == nil || out.Len() != 0 {
		t.Fatal("oversized child output retained")
	}
}

func TestQualificationStopsWhenCleanupIsUncertain(t *testing.T) {
	artifacts := []artifact{{path: "control"}, {path: "daemon"}, {path: "up-control"}, {path: "up-daemon"}}
	report, err := qualify(context.Background(), artifacts, func(_ context.Context, _, action, _ string) (keychaincheck.Result, error) {
		if action == "delete" {
			return keychaincheck.Result{}, errors.New("delete failed")
		}
		return keychaincheck.Result{OK: true, Category: "ok"}, nil
	})
	if err == nil || report.OK || len(report.Checks) != 1 || report.Checks[0].Cleanup != "failed" || report.Checks[0].OK {
		t.Fatalf("cleanup failure accepted or more items created: %+v %v", report, err)
	}
}

func TestQualificationRejectsUnsafeArtifactDirectories(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	link := directory + "-link"
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	for _, path := range []string{"relative", directory, link} {
		if _, err := inspectArtifacts(context.Background(), path, path); err == nil {
			t.Fatalf("unsafe artifacts accepted: %s", path)
		}
	}
}
