package telegram

import (
	"context"
	"errors"

	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

type concurrencyLimiter struct {
	slots chan struct{}
}

func newConcurrencyLimiter(maximum int) *concurrencyLimiter {
	if maximum < 1 {
		maximum = 1
	}
	return &concurrencyLimiter{slots: make(chan struct{}, maximum)}
}

func (l *concurrencyLimiter) Handle(next tg.Invoker) gotdtelegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		if l == nil || l.slots == nil {
			return errors.New("request concurrency limiter is not initialized")
		}
		select {
		case l.slots <- struct{}{}:
			defer func() { <-l.slots }()
		case <-ctx.Done():
			return ctx.Err()
		}
		return next.Invoke(ctx, input, output)
	}
}
