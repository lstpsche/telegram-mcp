package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

const testScopeID = "tgscope:v1:0123456789abcdef0123456789abcdef"

func TestScopeCommandsRoundTripStableIDWithoutTerminal(t *testing.T) {
	control := &fakeScopeController{}
	for _, args := range [][]string{
		{"scope", "--name", "work", "--peer", "tgpeer:v1:chat:123", "--peer", "tgpeer:v1:user:456"},
		{"scopes"},
		{"scope", "--id", testScopeID, "--name", "renamed"},
		{"unscope", "--id", testScopeID},
	} {
		var stdout, stderr bytes.Buffer
		code := runContext(context.Background(), args, &stdout, &stderr,
			func() (controller, error) { return control, nil },
			func() (terminal, error) {
				t.Fatal("scope command opened credential terminal")
				return nil, errors.New("unexpected")
			})
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("command=%s code=%d stderr=%q", args[0], code, stderr.String())
		}
		switch args[0] {
		case "scope":
			var scope policy.Scope
			if err := json.Unmarshal(stdout.Bytes(), &scope); err != nil {
				t.Fatal(err)
			}
			if scope.ID.String() != testScopeID || scope.Name != control.saved.Name || scope.Peers == nil {
				t.Fatalf("scope=%+v", scope)
			}
		case "scopes":
			var scopes []policy.Scope
			if err := json.Unmarshal(stdout.Bytes(), &scopes); err != nil || len(scopes) != 1 || len(scopes[0].Peers) != 2 {
				t.Fatalf("scopes=%+v error=%v", scopes, err)
			}
		case "unscope":
			if control.removed.String() != testScopeID {
				t.Fatal("stable ID not passed to deletion")
			}
		}
	}
	if control.saved.Name != "renamed" || len(control.saved.Peers) != 0 || control.inputID.String() != testScopeID {
		t.Fatalf("empty rename not passed exactly: %+v", control)
	}
}

func TestScopeArgumentsRejectAmbiguityBeforeController(t *testing.T) {
	invalid := [][]string{
		{"scope"}, {"scope", "--id", testScopeID}, {"scope", "--name"},
		{"scope", "--name", "Work"}, {"scope", "--name", ""}, {"scope", "--name", "null\x00"},
		{"scope", "--name", "work", "--name", "work"},
		{"scope", "--name", "work", "--id", testScopeID, "--id", testScopeID},
		{"scope", "--name", "work", "--id", "null"},
		{"scope", "--name", "work", "--peer", "tgpeer:v1:chat:0123"},
		{"scope", "--name", "work", "--peer", "tgpeer:v1:chat:123", "--peer", "tgpeer:v1:chat:123"},
		{"scope", "--name", "work", "--unknown", "value"},
		{"scope", "--name", "work", "positional"},
		{"scopes", "--name", "work"}, {"unscope"}, {"unscope", "--id", "work"},
		{"unscope", "--id", testScopeID, "--id", testScopeID},
	}
	oversized := []string{"scope", "--name", "work"}
	for index := 1; index <= policy.MaximumScopePeers+1; index++ {
		oversized = append(oversized, "--peer", fmt.Sprintf("tgpeer:v1:chat:%d", index))
	}
	invalid = append(invalid, oversized)
	for _, args := range invalid {
		control := &fakeScopeController{}
		var stdout, stderr bytes.Buffer
		if code := runScopeCommand(context.Background(), args, &stdout, &stderr, control); code != 2 || control.calls != 0 || stdout.Len() != 0 {
			t.Fatalf("args=%q code=%d calls=%d output=%q", args, code, control.calls, stdout.String())
		}
	}
	if _, _, peers, ok := parseScope(oversized[1 : len(oversized)-2]); !ok || len(peers) != policy.MaximumScopePeers {
		t.Fatal("maximum supported membership rejected")
	}
}

func TestScopeCommandsDoNotReleaseResultsOnFailure(t *testing.T) {
	const hostile = "private\x1b[31msecret"
	for _, args := range [][]string{{"scopes"}, {"scope", "--name", "work"}, {"unscope", "--id", testScopeID}} {
		control := &fakeScopeController{err: errors.New(hostile)}
		var stdout, stderr bytes.Buffer
		if code := runScopeCommand(context.Background(), args, &stdout, &stderr, control); code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), hostile) {
			t.Fatalf("command=%s code=%d stdout=%q stderr=%q", args[0], code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runScopeCommand(context.Background(), []string{"scope", "--name", hostile}, &stdout, &stderr, &fakeScopeController{}); code != 2 || strings.Contains(stderr.String(), hostile) {
		t.Fatal("hostile input echoed or accepted")
	}
}

func TestScopeListEmptyAndUnsupportedController(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runScopeCommand(context.Background(), []string{"scopes"}, &stdout, &stderr, &fakeScopeController{}); code != 0 || stdout.String() != "[]\n" {
		t.Fatalf("empty list code=%d output=%q", code, stdout.String())
	}
	stdout.Reset()
	if code := runScopeCommand(context.Background(), []string{"scopes"}, &stdout, &stderr, &fakeController{}); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "scope control is unavailable") {
		t.Fatalf("unsupported code=%d stderr=%q", code, stderr.String())
	}
}

type fakeScopeController struct {
	fakeController
	saved   policy.Scope
	inputID model.ScopeID
	removed model.ScopeID
	calls   int
	err     error
}

func (f *fakeScopeController) Scopes(context.Context) ([]policy.Scope, error) {
	f.calls++
	if f.saved.ID == "" {
		return nil, f.err
	}
	return []policy.Scope{f.saved}, f.err
}

func (f *fakeScopeController) Scope(_ context.Context, id model.ScopeID, name string, peers []model.PeerID) (policy.Scope, error) {
	f.calls++
	f.inputID = id
	f.saved = policy.Scope{ID: model.ScopeID(testScopeID), Name: name, Peers: peers}
	return f.saved, f.err
}

func (f *fakeScopeController) Unscope(_ context.Context, id model.ScopeID) error {
	f.calls++
	f.removed = id
	return f.err
}
