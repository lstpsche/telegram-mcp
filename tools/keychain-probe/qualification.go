package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/keychaincheck"
	"golang.org/x/sys/unix"
)

type artifact struct {
	Role   string `json:"role"`
	SHA256 string `json:"sha256"`
	path   string
}

type check struct {
	Name         string `json:"name"`
	OK           bool   `json:"ok"`
	FailedAction string `json:"failed_action,omitempty"`
	Category     string `json:"category,omitempty"`
	NativeStatus int32  `json:"native_status,omitempty"`
	Cleanup      string `json:"cleanup"`
}

type qualification struct {
	ArtifactsUnchanged bool       `json:"artifacts_unchanged"`
	OK                 bool       `json:"ok"`
	RequiredChecks     int        `json:"required_checks"`
	Artifacts          []artifact `json:"artifacts"`
	Checks             []check    `json:"checks"`
}

type invoke func(context.Context, string, string, string) (keychaincheck.Result, error)

func runQualification(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 4 || args[0] != "--bin-dir" || args[2] != "--upgrade-bin-dir" {
		fmt.Fprintln(stderr, "keychain qualification: use --bin-dir ABSOLUTE_DIRECTORY --upgrade-bin-dir ABSOLUTE_DIRECTORY")
		return 2
	}
	artifacts, err := inspectArtifacts(ctx, args[1], args[3])
	if err != nil {
		fmt.Fprintln(stderr, "keychain qualification: artifact validation failed; require private canonical directories, signed owned executables and changed upgrade bytes")
		return 1
	}
	report, err := qualify(ctx, artifacts, invokeWorker)
	after, validationErr := inspectArtifacts(ctx, args[1], args[3])
	report.ArtifactsUnchanged = validationErr == nil && len(after) == len(artifacts)
	if report.ArtifactsUnchanged {
		for i := range artifacts {
			if after[i].SHA256 != artifacts[i].SHA256 {
				report.ArtifactsUnchanged = false
			}
		}
	}
	if !report.ArtifactsUnchanged {
		report.OK = false
		err = errors.Join(err, errors.New("artifacts changed or final validation failed"), validationErr)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		fmt.Fprintln(stderr, "keychain qualification: cannot write results")
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "keychain qualification failed; inspect the fixed result categories and cleanup outcomes")
		return 1
	}
	return 0
}

func inspectArtifacts(ctx context.Context, current, upgrade string) ([]artifact, error) {
	var artifacts []artifact
	for index, dir := range []string{current, upgrade} {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || resolved != dir {
			return nil, errors.New("artifact directory is not canonical")
		}
		if err := inspectArtifactAncestors(dir); err != nil {
			return nil, err
		}
		var directory unix.Stat_t
		if err := unix.Lstat(dir, &directory); err != nil {
			return nil, err
		}
		if directory.Uid != uint32(os.Geteuid()) || directory.Mode != unix.S_IFDIR|0700 {
			return nil, errors.New("artifact directory is not private")
		}
		for _, name := range []string{"telegram-mcpctl", "telegram-mcpd"} {
			path := filepath.Join(dir, name)
			var info unix.Stat_t
			if err := unix.Lstat(path, &info); err != nil {
				return nil, err
			}
			if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uint32(os.Geteuid()) || info.Mode&0022 != 0 || info.Mode&0100 == 0 || info.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 {
				return nil, errors.New("artifact is not an owned executable")
			}
			command := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", path)
			command.Env = []string{"PATH=/usr/bin:/bin"}
			if err := command.Run(); err != nil {
				return nil, fmt.Errorf("verify artifact signature: %w", err)
			}
			file, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			digest := sha256.New()
			count, readErr := io.Copy(digest, io.LimitReader(file, 256*1024*1024+1))
			if err := errors.Join(readErr, file.Close()); err != nil {
				return nil, err
			}
			if count == 0 || count > 256*1024*1024 {
				return nil, errors.New("artifact size is invalid")
			}
			role := []string{"current_", "upgrade_"}[index] + map[string]string{"telegram-mcpctl": "control", "telegram-mcpd": "daemon"}[name]
			artifacts = append(artifacts, artifact{Role: role, SHA256: hex.EncodeToString(digest.Sum(nil)), path: path})
		}
	}
	if artifacts[0].SHA256 == artifacts[2].SHA256 || artifacts[1].SHA256 == artifacts[3].SHA256 {
		return nil, errors.New("upgrade artifacts must differ")
	}
	return artifacts, nil
}

func qualify(ctx context.Context, artifacts []artifact, run invoke) (qualification, error) {
	pairs := []struct {
		name            string
		creator, reader int
	}{
		{"control_restart", 0, 0}, {"daemon_restart", 1, 1},
		{"control_to_daemon", 0, 1}, {"daemon_to_control", 1, 0},
		{"control_upgrade", 0, 2}, {"daemon_upgrade", 1, 3},
		{"upgraded_control_to_daemon", 2, 3}, {"upgraded_daemon_to_control", 3, 2},
	}
	report := qualification{RequiredChecks: len(pairs), Artifacts: artifacts, Checks: make([]check, 0, len(pairs))}
	var failures error
	for _, pair := range pairs {
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return report, errors.Join(failures, err)
		}
		outcome, err := exercise(ctx, pair.name, artifacts[pair.creator].path, artifacts[pair.reader].path, hex.EncodeToString(token), run)
		clear(token)
		report.Checks = append(report.Checks, outcome)
		failures = errors.Join(failures, err)
		if outcome.Cleanup == "failed" || ctx.Err() != nil {
			return report, errors.Join(failures, ctx.Err())
		}
	}
	report.OK = failures == nil
	return report, failures
}

func exercise(ctx context.Context, name, creator, reader, token string, run invoke) (outcome check, result error) {
	outcome = check{Name: name, Cleanup: "not_created"}
	// Even a failed create may have written before a transport failure. The creator
	// performs cleanup with its own deadline, independent of caller cancellation.
	defer func() {
		if outcome.Category == "already_exists" && outcome.FailedAction == "create" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, action := range []string{"delete", "absent"} {
			reply, err := run(cleanup, creator, action, token)
			if err != nil || !reply.OK {
				outcome.OK = false
				outcome.Cleanup = "failed"
				result = errors.Join(result, errors.New("synthetic item cleanup failed"), err)
				return
			}
		}
		outcome.Cleanup = "verified_absent"
	}()
	for _, step := range []struct{ binary, action string }{{creator, "create"}, {reader, "read"}, {reader, "update"}, {creator, "verify"}} {
		reply, err := run(ctx, step.binary, step.action, token)
		if err != nil || !reply.OK {
			outcome.FailedAction = step.action
			if err != nil {
				outcome.Category = "process_failure"
				if errors.Is(err, context.DeadlineExceeded) {
					outcome.Category = "worker_timeout"
				} else if errors.Is(err, context.Canceled) {
					outcome.Category = "cancelled"
				}
			} else {
				outcome.Category = reply.Category
				outcome.NativeStatus = reply.NativeStatus
			}
			return outcome, errors.Join(errors.New("native identity check failed"), err)
		}
	}
	outcome.OK = true
	return outcome, nil
}

type boundedOutput struct{ buffer bytes.Buffer }

func (w *boundedOutput) Len() int      { return w.buffer.Len() }
func (w *boundedOutput) Bytes() []byte { return w.buffer.Bytes() }

func (w *boundedOutput) Write(data []byte) (int, error) {
	if w.Len()+len(data) > 2048 {
		return 0, errors.New("worker output exceeds limit")
	}
	return w.buffer.Write(data)
}

func invokeWorker(ctx context.Context, path, action, token string) (keychaincheck.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return keychaincheck.Result{}, err
	}
	command := exec.CommandContext(ctx, path, "--keychain-probe", action, token)
	command.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C", "TMPDIR=/tmp"}
	var stdout, stderr boundedOutput
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return keychaincheck.Result{}, errors.Join(errors.New("worker process failed"), err, ctx.Err())
	}
	if stderr.Len() != 0 {
		return keychaincheck.Result{}, errors.New("worker returned unexpected diagnostics")
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	var reply keychaincheck.Result
	if decodeErr := decoder.Decode(&reply); decodeErr != nil {
		return reply, fmt.Errorf("decode worker response: %w", decodeErr)
	}
	if decodeErr := decoder.Decode(&struct{}{}); !errors.Is(decodeErr, io.EOF) {
		return reply, errors.New("worker returned extra output")
	}
	if !validReply(reply) || reply.OK != (err == nil) {
		return reply, errors.New("worker response disagrees with exit status")
	}
	return reply, nil
}

func validReply(reply keychaincheck.Result) bool {
	if reply.OK {
		return reply.Category == "ok" && reply.NativeStatus == 0
	}
	switch reply.Category {
	case "invalid_input", "cancelled", "already_exists", "value_mismatch", "still_present", "not_found", "interaction_unavailable", "wrong_keychain", "unsupported", "native_failure":
		return true
	default:
		return false
	}
}

func inspectArtifactAncestors(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		var info unix.Stat_t
		if err := unix.Lstat(parent, &info); err != nil {
			return err
		}
		stickyRoot := info.Uid == 0 && info.Mode&unix.S_ISVTX != 0
		if info.Mode&unix.S_IFMT != unix.S_IFDIR || (info.Uid != 0 && info.Uid != uint32(os.Geteuid())) || (info.Mode&0022 != 0 && !stickyRoot) {
			return errors.New("unsafe artifact ancestry")
		}
		if parent == "/" {
			return nil
		}
	}
}
