package app

import (
	"context"
	"errors"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/policy"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestFullReadControlUsesPolicyWithoutOpeningCredentialsOrTelegram(t *testing.T) {
	application := authorizedTextApplication(t, &fakeDiscoveryRuntime{})
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		t.Fatal("policy opened account runtime")
		return nil, errors.New("unexpected")
	}
	ctx := context.Background()
	for _, enabled := range []bool{true, false} {
		if err := application.SetFullRead(ctx, enabled); err != nil {
			t.Fatal(err)
		}
		actual, err := application.FullRead(ctx)
		if err != nil || actual != enabled {
			t.Fatal(actual, err)
		}
	}
	if err := application.withPolicy(ctx, func(_ *policy.Lease) error {
		if err := application.SetFullRead(ctx, true); !errors.Is(err, policy.ErrBusy) {
			t.Fatal("mutation raced content lease", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFullReadControlRequiresAuthorizationEpoch(t *testing.T) {
	application, _ := newTestApplication(t)
	if err := application.SetFullRead(context.Background(), true); !errors.Is(err, policy.ErrEpochChanged) {
		t.Fatal("unconfigured account enabled", err)
	}
}
