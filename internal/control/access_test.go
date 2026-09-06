package control

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeAccessController struct {
	fakeController
	full      bool
	writes    int
	accessErr error
}

func (f *fakeAccessController) FullRead(context.Context) (bool, error) { return f.full, f.accessErr }
func (f *fakeAccessController) SetFullRead(_ context.Context, enabled bool) error {
	if f.accessErr != nil {
		return f.accessErr
	}
	f.full = enabled
	f.writes++
	return nil
}

func TestAccessCommandRequiresExplicitEnableAndSupportsRevocation(t *testing.T) {
	f := &fakeAccessController{}
	for _, args := range [][]string{{"access"}, {"access", "full", "--accept-full-read"}, {"access"}, {"access", "restricted"}} {
		var out, errout bytes.Buffer
		if code := runContext(context.Background(), args, &out, &errout, func() (controller, error) { return f, nil }, func() (terminal, error) { t.Fatal("opened credential terminal"); return nil, errors.New("unexpected") }); code != 0 {
			t.Fatal(code, errout.String())
		}
		if len(args) == 1 && !strings.Contains(out.String(), `"mode"`) {
			t.Fatal("missing current mode")
		}
	}
	if f.full || f.writes != 2 {
		t.Fatal("mode lifecycle", f.full, f.writes)
	}
	for _, args := range [][]string{{"access", "full"}, {"access", "full", "--accept-full-read", "--accept-full-read"}, {"access", "restricted", "extra"}, {"access", "secret"}} {
		var out, errout bytes.Buffer
		if code := runAccessCommand(context.Background(), args, &out, &errout, f); code != 2 || out.Len() != 0 {
			t.Fatal("invalid mutation accepted", code)
		}
		if strings.Contains(errout.String(), "secret") {
			t.Fatal("raw args disclosed")
		}
	}
	f.accessErr = errors.New("private detail")
	var out, errout bytes.Buffer
	if code := runAccessCommand(context.Background(), []string{"access", "full", "--accept-full-read"}, &out, &errout, f); code != 1 || out.Len() != 0 || strings.Contains(errout.String(), "private detail") {
		t.Fatal("unsafe failure")
	}
}
