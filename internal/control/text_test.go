package control

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func grantArguments() []string {
	return []string{"--peer", "tgpeer:v1:chat:123", "--author", "tgpeer:v1:user:456", "--min-id", "100", "--max-id", "120", "--read-through", "120", "--expires-at", "2026-09-06T12:00:00Z", "--profile", "consented", "--attest-eligible"}
}

func TestParseGrantRequiresExplicitCanonicalScope(t *testing.T) {
	args := grantArguments()
	grant, ok := parseGrant(args)
	if !ok || grant.Peer.String() != args[1] || grant.Author.String() != args[3] || grant.MinID != 100 || grant.MaxID != 120 || grant.ReadThrough != 120 || !grant.Eligible {
		t.Fatalf("valid grant rejected: %+v, %t", grant, ok)
	}
	for _, change := range []struct {
		index int
		value string
	}{
		{1, "@someone"}, {1, "tgpeer:v2:chat:123"}, {1, "-100123"}, {3, "tgpeer:v1:self:456"},
		{5, "0"}, {5, "0100"}, {5, "+100"}, {5, "2147483648"}, {7, "99"},
		{9, "-1"}, {9, "0120"}, {11, "tomorrow"}, {13, "assumed"},
	} {
		t.Run(change.value, func(t *testing.T) {
			changed := append([]string(nil), args...)
			changed[change.index] = change.value
			if _, ok := parseGrant(changed); ok {
				t.Fatal("invalid grant accepted")
			}
		})
	}
	for _, invalid := range [][]string{args[:len(args)-1], append(append([]string(nil), args...), "--attest-eligible"), append(append([]string(nil), args...), "--peer", args[1]), append(append([]string(nil), args...), "secret"), nil} {
		if _, ok := parseGrant(invalid); ok {
			t.Fatal("incomplete or ambiguous grant accepted")
		}
	}
	args[9] = "0"
	if grant, ok := parseGrant(args); !ok || grant.ReadThrough != 0 {
		t.Fatal("explicit no-read ceiling rejected")
	}
}

func TestSavedMessageCommandReturnsOnlyReferenceWithoutTerminal(t *testing.T) {
	for _, fail := range []bool{false, true} {
		control := &fakeTextController{}
		if fail {
			control.err = errors.New("private upstream details")
		}
		var stdout, stderr bytes.Buffer
		code := runContext(context.Background(), []string{"saved-message"}, &stdout, &stderr,
			func() (controller, error) { return control, nil },
			func() (terminal, error) {
				t.Fatal("discovery opened credential terminal")
				return nil, errors.New("unexpected")
			})
		if fail {
			if code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "private") {
				t.Fatal("unsafe discovery failure output")
			}
		} else if code != 0 || stdout.String() != "\"tgmsg:v1:self:456:120\"\n" || stderr.Len() != 0 {
			t.Fatal("unexpected discovery output", code, stdout.String(), stderr.String())
		}
	}
}

func TestGrantCommandPassesExactScopeWithoutTerminal(t *testing.T) {
	control := &fakeTextController{}
	var stdout, stderr bytes.Buffer
	args := append([]string{"grant"}, grantArguments()...)
	code := runContext(context.Background(), args, &stdout, &stderr,
		func() (controller, error) { return control, nil },
		func() (terminal, error) {
			t.Fatal("grant opened credential terminal")
			return nil, errors.New("unexpected")
		})
	if code != 0 || stderr.Len() != 0 || control.saved.Author.String() != "tgpeer:v1:user:456" || !control.saved.Eligible {
		t.Fatalf("code=%d stderr=%s grant=%+v", code, stderr.String(), control.saved)
	}
}

func TestTextCommandsDoNotEchoHostileArgumentsOrErrors(t *testing.T) {
	const hostile = "private\x1b[31msecret"
	for _, args := range [][]string{{"peers", hostile}, {"saved-message", hostile}, {"revoke", "--peer", hostile}, {"grant", "--peer", hostile}} {
		var stdout, stderr bytes.Buffer
		if code := runTextCommand(context.Background(), args, &stdout, &stderr, &fakeTextController{}); code != 2 {
			t.Fatalf("code=%d", code)
		}
		if strings.Contains(stdout.String()+stderr.String(), hostile) {
			t.Fatal("hostile argument echoed")
		}
	}
	var stdout, stderr bytes.Buffer
	control := &fakeTextController{err: errors.New(hostile)}
	if code := runTextCommand(context.Background(), []string{"peers"}, &stdout, &stderr, control); code != 1 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q", code, stdout.String())
	}
	if strings.Contains(stderr.String(), hostile) {
		t.Fatal("raw failure echoed")
	}
}

func TestPeerDisplayEscapesTerminalControls(t *testing.T) {
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	control := &fakeTextController{peers: []model.Chat{{ID: peer, Title: "name\x1b[31m\nnext\u009b\u007f"}}}
	var stdout, stderr bytes.Buffer
	if code := runTextCommand(context.Background(), []string{"peers"}, &stdout, &stderr, control); code != 0 {
		t.Fatal(stderr.String())
	}
	if strings.ContainsAny(stdout.String(), "\x1b\u009b\u007f") || !strings.Contains(stdout.String(), `\u001b`) || strings.Count(stdout.String(), "\n") != 1 {
		t.Fatalf("unsafe output=%q", stdout.String())
	}
}

func TestPolicyContentionProvidesSpecificDiagnostic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	control := &fakeTextController{err: policy.ErrBusy}
	if code := runTextCommand(context.Background(), []string{"grants"}, &stdout, &stderr, control); code != 1 || !strings.Contains(stderr.String(), "text policy is busy") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestTextCommandRequiresControllerCapability(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runTextCommand(context.Background(), []string{"peers"}, &stdout, &stderr, &fakeController{}); code != 1 || !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

type fakeTextController struct {
	fakeController
	peers   []model.Chat
	saved   policy.Grant
	revoked model.PeerID
	err     error
}

func (f *fakeTextController) SavedMessage(context.Context) (model.MessageID, error) {
	message, _ := model.ParseMessageID("tgmsg:v1:self:456:120")
	return message, f.err
}

func (f *fakeTextController) Peers(context.Context) ([]model.Chat, error) { return f.peers, f.err }
func (f *fakeTextController) Grants(context.Context) ([]policy.Grant, error) {
	return []policy.Grant{}, f.err
}
func (f *fakeTextController) Grant(_ context.Context, grant policy.Grant) error {
	f.saved = grant
	return f.err
}
func (f *fakeTextController) Revoke(_ context.Context, peer model.PeerID) error {
	f.revoked = peer
	return f.err
}

func TestRevokeValidatesTypedPeerBeforeMutation(t *testing.T) {
	control := &fakeTextController{}
	var stdout, stderr bytes.Buffer
	code := runTextCommand(context.Background(), []string{"revoke", "--peer", "tgpeer:v1:chat:123"}, &stdout, &stderr, control)
	if code != 0 || control.revoked.String() != "tgpeer:v1:chat:123" {
		t.Fatalf("code=%d revoked=%s stderr=%q", code, control.revoked.String(), stderr.String())
	}
	control.revoked = model.PeerID{}
	code = runTextCommand(context.Background(), []string{"revoke", "--peer", "@name"}, &stdout, &stderr, control)
	if code != 2 || control.revoked.String() != "" {
		t.Fatal("invalid peer reached revoke")
	}
}

func TestEmptyHumanListsUseArrays(t *testing.T) {
	for _, command := range []string{"peers", "grants"} {
		var stdout, stderr bytes.Buffer
		if code := runTextCommand(context.Background(), []string{command}, &stdout, &stderr, &fakeTextController{}); code != 0 || stdout.String() != "[]\n" {
			t.Fatalf("%s: code=%d output=%q stderr=%q", command, code, stdout.String(), stderr.String())
		}
	}
}

func TestGrantJSONKeepsIdentifiersAsStrings(t *testing.T) {
	grant, ok := parseGrant(grantArguments())
	if !ok {
		t.Fatal("invalid fixture")
	}
	var output bytes.Buffer
	if err := writeTextJSON(&output, newGrantRecord(grant)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"peer":"tgpeer:v1:chat:123"`, `"author":"tgpeer:v1:user:456"`, `"min_id":"100"`, `"max_id":"120"`, `"read_through":"120"`} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("missing string identifier %s in %s", field, output.String())
		}
	}
}

func TestGrantImagesRequireStandaloneExplicitFlag(t *testing.T) {
	args := grantArguments()
	grant, ok := parseGrant(args)
	if !ok || grant.Images {
		t.Fatal("text grant implicitly enabled images")
	}
	grant, ok = parseGrant(append(append([]string(nil), args...), "--allow-images"))
	if !ok || !grant.Images {
		t.Fatal("explicit image grant rejected")
	}
	for _, suffix := range [][]string{{"--allow-images", "--allow-images"}, {"--allow-images=true"}, {"--allow-images", "true"}, {"--allow-images", "false"}} {
		if _, ok := parseGrant(append(append([]string(nil), args...), suffix...)); ok {
			t.Fatal("ambiguous image flag accepted")
		}
	}
	control := &fakeTextController{}
	for _, enabled := range []bool{true, false} {
		command := append([]string{"grant"}, args...)
		if enabled {
			command = append(command, "--allow-images")
		}
		var stdout, stderr bytes.Buffer
		if code := runTextCommand(context.Background(), command, &stdout, &stderr, control); code != 0 || control.saved.Images != enabled {
			t.Fatalf("image permission did not reach controller: code=%d grant=%+v", code, control.saved)
		}
		var output bytes.Buffer
		if err := writeTextJSON(&output, newGrantRecord(control.saved)); err != nil {
			t.Fatal(err)
		}
		expected := `"images":false`
		if enabled {
			expected = `"images":true`
		}
		if !strings.Contains(output.String(), expected) {
			t.Fatal("grant JSON omitted image permission")
		}
	}
}

func TestChannelGrantRequiresMatchingPublisherAndConsent(t *testing.T) {
	args := grantArguments()
	args[1], args[3] = "tgpeer:v1:channel:42", "tgpeer:v1:channel:42"
	if _, ok := parseGrant(args); !ok {
		t.Fatal("channel publisher rejected")
	}
	for _, change := range []struct {
		index int
		value string
	}{{3, "tgpeer:v1:channel:99"}, {13, "self-authored"}, {1, "tgpeer:v1:channel:42:topic:7"}} {
		invalid := append([]string(nil), args...)
		invalid[change.index] = change.value
		if _, ok := parseGrant(invalid); ok {
			t.Fatal("invalid publisher authority accepted")
		}
	}
}
