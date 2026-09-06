package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type scopeSetupControl struct {
	fakeTextController
	scopes       fakeScopeController
	saveError    error
	grantsError  error
	scopeWrites  int
	accessWrites int
}

func (c *scopeSetupControl) Scopes(ctx context.Context) ([]policy.Scope, error) {
	return c.scopes.Scopes(ctx)
}
func (c *scopeSetupControl) Scope(ctx context.Context, id model.ScopeID, name string, peers []model.PeerID) (policy.Scope, error) {
	c.scopeWrites++
	if c.saveError != nil {
		return policy.Scope{}, c.saveError
	}
	return c.scopes.Scope(ctx, id, name, peers)
}
func (c *scopeSetupControl) Unscope(ctx context.Context, id model.ScopeID) error {
	return c.scopes.Unscope(ctx, id)
}
func (c *scopeSetupControl) Grants(context.Context) ([]policy.Grant, error) {
	if c.grantsError != nil {
		return nil, c.grantsError
	}
	if c.saved.Eligible {
		return []policy.Grant{c.saved}, nil
	}
	return []policy.Grant{}, nil
}
func (c *scopeSetupControl) FullRead(context.Context) (bool, error) { return false, nil }
func (c *scopeSetupControl) SetFullRead(context.Context, bool) error {
	c.accessWrites++
	return nil
}
func (c *scopeSetupControl) Peers(context.Context) ([]model.Chat, error) {
	return nil, errors.New("unexpected Telegram discovery")
}

func TestFreshSetupConnectsGrantedPeerToScope(t *testing.T) {
	control := &scopeSetupControl{}
	prompt := &setupAnswers{fakeTerminal: fakeTerminal{apiID: 12, apiHash: []byte("secret")}, answers: []string{
		"test2", "phone", "restricted", "saved", "yes", "24", "0", "no", "yes",
		"yes", "project", "1", "yes", "json",
	}}
	manager := &setupService{}
	calls := 0
	ready := true
	var output bytes.Buffer
	s := setupSession{control: control, prompt: prompt, output: &output, binDir: "/private/bin", local: support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
		calls++
		if calls == 1 {
			return diagnostics.Report{}, nil
		}
		if control.scopeWrites != 1 {
			t.Fatal("service started before scope was saved")
		}
		return diagnostics.Report{MCP: true, MessageReads: &ready}, nil
	}}}
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(control.scopes.saved.Peers, []model.PeerID{control.saved.Peer}) || control.scopes.saved.Name != "project" || control.accessWrites != 0 || control.saved.ReadThrough != 0 || control.saved.Images {
		t.Fatal("scope or authority differs from confirmed choices", control)
	}
	if len(prompt.answers) != 0 || !reflect.DeepEqual(manager.actions, []string{"install", "start"}) || !strings.Contains(output.String(), `"mcpServers"`) || strings.Contains(output.String(), "secret") {
		t.Fatal("incomplete or unsafe setup", manager.actions, output.String())
	}
}

func TestScopeSetupReplacesExactMembershipAndRetainsIdentity(t *testing.T) {
	old, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	added, _ := model.ParsePeerID("tgpeer:v1:user:456")
	for _, selection := range []string{"1 tgpeer:v1:user:456", "empty"} {
		control := &scopeSetupControl{scopes: fakeScopeController{saved: policy.Scope{ID: testScopeID, Name: "project", Peers: []model.PeerID{old}}}}
		var output bytes.Buffer
		s := setupSession{control: control, prompt: &setupAnswers{answers: []string{"project", selection, "yes"}}, output: &output}
		if err := s.guideScope(context.Background()); err != nil {
			t.Fatal(err)
		}
		want := []model.PeerID{}
		if selection != "empty" {
			want = []model.PeerID{old, added}
		}
		if control.scopes.inputID != testScopeID || !reflect.DeepEqual(control.scopes.saved.Peers, want) || control.scopeWrites != 1 || control.accessWrites != 0 || control.saved.Eligible {
			t.Fatal("incorrect replacement", control)
		}
	}
}

func TestScopeSetupRefusalAndInvalidInputPreserveMembership(t *testing.T) {
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	original := policy.Scope{ID: testScopeID, Name: "project", Peers: []model.PeerID{peer}}
	for _, answers := range [][]string{
		{"project", "empty", "no"}, {"project", ""}, {"project", "1 1"},
		{"project", "1 tgpeer:v1:chat:123"}, {"project", "@mutable_name"},
		{"project", "2"}, {"project", "01"}, {"Private\x1b[2J"}, {"project"},
	} {
		control := &scopeSetupControl{scopes: fakeScopeController{saved: original}}
		var output bytes.Buffer
		s := setupSession{control: control, prompt: &setupAnswers{answers: answers}, output: &output}
		if err := s.guideScope(context.Background()); err == nil || control.scopeWrites != 0 || !reflect.DeepEqual(original, control.scopes.saved) || strings.Contains(output.String(), "Private") {
			t.Fatal("unsafe failed setup", answers, err)
		}
	}
}

func TestScopeSelectionLimits(t *testing.T) {
	values := make([]string, policy.MaximumScopePeers+1)
	for i := range values {
		peer, _ := model.NewPeerID(model.PeerKindChat, int64(i+1))
		values[i] = peer.String()
	}
	if _, err := scopeSelection(strings.Join(values, " "), nil); err == nil {
		t.Fatal("oversized selection accepted")
	}
	if peers, err := scopeSelection(strings.Join(values[:policy.MaximumScopePeers], " "), nil); err != nil || len(peers) != policy.MaximumScopePeers {
		t.Fatal("maximum membership rejected", err)
	}
}

func TestScopeSetupDispatchAndMetadataFailures(t *testing.T) {
	for _, fail := range []string{"", "scopes", "grants", "terminal", "arguments", "cancel"} {
		t.Run(fail, func(t *testing.T) {
			control := &scopeSetupControl{}
			failure := errors.New("private external details")
			if fail == "scopes" {
				control.scopes.err = failure
			}
			if fail == "grants" {
				control.grantsError = failure
			}
			args := []string{"scope", "setup"}
			if fail == "arguments" {
				args = append(args, "unexpected")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fail == "cancel" {
				cancel()
			}
			var output, errout bytes.Buffer
			code := runContext(ctx, args, &output, &errout, func() (controller, error) { return control, nil }, func() (terminal, error) {
				if fail == "arguments" {
					t.Fatal("invalid arguments opened terminal")
				}
				if fail == "terminal" {
					return nil, failure
				}
				return &setupAnswers{answers: []string{"project", "empty", "yes"}}, nil
			})
			if fail == "" {
				if code != 0 || control.scopeWrites != 1 || errout.Len() != 0 {
					t.Fatal(code, errout.String())
				}
			} else if code == 0 || control.scopeWrites != 0 || strings.Contains(errout.String(), failure.Error()) {
				t.Fatal("failure changed state or disclosed details", code, errout.String())
			}
		})
	}
}

func TestScopeSetupPropagatesOutputFailureBeforeSave(t *testing.T) {
	control := &scopeSetupControl{}
	s := setupSession{control: control, prompt: &setupAnswers{answers: []string{"project", "empty", "yes"}}, output: closedScopeOutput{}}
	if err := s.guideScope(context.Background()); !errors.Is(err, io.ErrClosedPipe) || control.scopeWrites != 0 {
		t.Fatal("output failure ignored", err)
	}
}

type closedScopeOutput struct{}

func (closedScopeOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestScopeSetupSaveFailureDoesNotClaimSuccess(t *testing.T) {
	failure := errors.New("private storage details")
	control := &scopeSetupControl{saveError: failure}
	var output bytes.Buffer
	s := setupSession{control: control, prompt: &setupAnswers{answers: []string{"project", "empty", "yes"}}, output: &output}
	if err := s.guideScope(context.Background()); !errors.Is(err, failure) || control.scopes.saved.ID != "" || strings.Contains(output.String(), "Scope saved") {
		t.Fatal("save failure hidden", err, output.String())
	}
}

func TestScopeSetupDoesNotOfferDuplicateStoredPeers(t *testing.T) {
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	control := &scopeSetupControl{scopes: fakeScopeController{saved: policy.Scope{ID: testScopeID, Name: "project", Peers: []model.PeerID{peer}}}}
	control.saved = policy.Grant{Peer: peer, Eligible: true}
	var output bytes.Buffer
	s := setupSession{control: control, prompt: &setupAnswers{answers: []string{"project", "1", "yes"}}, output: &output}
	if err := s.guideScope(context.Background()); err != nil || strings.Count(output.String(), `"number":`) != 1 || len(control.scopes.saved.Peers) != 1 {
		t.Fatal("duplicate peer choices", err, output.String())
	}
}
