package control

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

const retainedOperationID = "01939ab2-f400-7000-8000-000000000001"

type fakeSendingController struct {
	fakeController
	enabled     bool
	writes      int
	inspections int
	statusReads int
	operationID string
	controlErr  error
	statusErr   error
}

func (f *fakeSendingController) FullSend(context.Context) (bool, error) {
	f.inspections++
	return f.enabled, f.controlErr
}

func (f *fakeSendingController) SetFullSend(_ context.Context, enabled bool) error {
	if f.controlErr != nil {
		return f.controlErr
	}
	f.enabled = enabled
	f.writes++
	return nil
}

func (f *fakeSendingController) SendOperationStatus(_ context.Context, operationID string) (model.SendOperation, error) {
	f.statusReads++
	f.operationID = operationID
	peer, _ := model.ParsePeerID("tgpeer:v1:user:123")
	return model.SendOperation{OperationID: operationID, Peer: peer, State: model.SendUncertain}, f.statusErr
}

func TestSendingControlRequiresExplicitOptInAndSupportsRevocation(t *testing.T) {
	f := &fakeSendingController{}
	for _, args := range [][]string{{"sending"}, {"sending", "enable", "--accept-account-wide-send"}, {"sending"}, {"sending", "disable"}} {
		var out, errout bytes.Buffer
		if code := runContext(context.Background(), args, &out, &errout, func() (controller, error) { return f, nil }, func() (terminal, error) {
			t.Fatal("opened credential terminal")
			return nil, errors.New("unexpected")
		}); code != 0 {
			t.Fatal(code, errout.String())
		}
		if len(args) == 1 && !strings.Contains(out.String(), `"enabled"`) {
			t.Fatal("missing sending mode")
		}
		if len(args) == 3 {
			for _, disclosure := range []string{"new conversations", "channel posts", "without per-recipient confirmation", "bots", "reply-routing metadata", "incidental message bodies", "never exposed or persisted", "no message-body access", "Read scopes do not restrict sending"} {
				if !strings.Contains(out.String(), disclosure) {
					t.Fatal("missing sending disclosure", disclosure)
				}
			}
		}
	}
	if f.enabled || f.writes != 2 || f.inspections != 2 {
		t.Fatalf("sending lifecycle: %+v", f)
	}
	for _, args := range [][]string{{"sending", "enable"}, {"sending", "enable", "--accept-account-wide-send", "--accept-account-wide-send"}, {"sending", "disable", "extra"}, {"sending", "inspect"}, {"sending", "private-input"}} {
		var out, errout bytes.Buffer
		if code := runSendingCommand(context.Background(), args, &out, &errout, f); code != 2 || out.Len() != 0 {
			t.Fatal("invalid mutation accepted", code)
		}
		if strings.Contains(errout.String(), "private-input") {
			t.Fatal("raw arguments disclosed")
		}
	}
	if f.writes != 2 || f.inspections != 2 {
		t.Fatal("invalid syntax reached policy control")
	}
	f.controlErr = errors.New("private detail")
	var out, errout bytes.Buffer
	if code := runSendingCommand(context.Background(), []string{"sending", "enable", "--accept-account-wide-send"}, &out, &errout, f); code != 1 || out.Len() != 0 || strings.Contains(errout.String(), "private detail") {
		t.Fatal("unsafe control failure")
	}
}

func TestSendStatusUsesOnlyLocalStatusControllerAndAcceptsOldOperationIDs(t *testing.T) {
	f := &fakeSendingController{controlErr: errors.New("policy must remain unused")}
	var out, errout bytes.Buffer
	args := []string{"sending", "status", "--operation-id", retainedOperationID}
	if code := runContext(context.Background(), args, &out, &errout, func() (controller, error) { return f, nil }, func() (terminal, error) {
		t.Fatal("opened credential terminal")
		return nil, errors.New("unexpected")
	}); code != 0 {
		t.Fatal(code, errout.String())
	}
	if f.statusReads != 1 || f.operationID != retainedOperationID || f.writes != 0 || f.inspections != 0 || !strings.Contains(out.String(), retainedOperationID) {
		t.Fatalf("status routing: %+v output=%q", f, out.String())
	}
	for _, args := range [][]string{{"sending", "status"}, {"sending", "status", "--operation-id", "private-input"}, {"sending", "status", "--operation-id", retainedOperationID, "extra"}, {"sending", "status", "--id", retainedOperationID}} {
		out.Reset()
		errout.Reset()
		if code := runSendingCommand(context.Background(), args, &out, &errout, f); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "private-input") {
			t.Fatal("invalid status syntax accepted or disclosed")
		}
	}
	if f.statusReads != 1 {
		t.Fatal("invalid syntax reached metadata lookup")
	}
}

func TestSendStatusUnavailableDoesNotClaimNotSent(t *testing.T) {
	f := &fakeSendingController{statusErr: errors.Join(app.ErrSendStatusUnavailable, errors.New("private detail"))}
	var out, errout bytes.Buffer
	if code := runSendingCommand(context.Background(), []string{"sending", "status", "--operation-id", retainedOperationID}, &out, &errout, f); code != 1 || out.Len() != 0 {
		t.Fatal("missing status succeeded", code)
	}
	if !strings.Contains(errout.String(), "does not prove a message was not sent") || strings.Contains(errout.String(), "private detail") || strings.Contains(errout.String(), `"not_sent"`) {
		t.Fatal("unsafe missing status disclosure", errout.String())
	}
}

func TestHelpDisclosesIndependentAccountWideSending(t *testing.T) {
	var output bytes.Buffer
	writeHelp(&output)
	for _, text := range []string{"sending enable --accept-account-wide-send", "sending status --operation-id UUID", "Full read access does not enable sending", sendingDisclosure} {
		if !strings.Contains(output.String(), text) {
			t.Fatal("missing sending help", text)
		}
	}
}
