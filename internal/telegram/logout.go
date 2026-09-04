package telegram

import "context"

// Logout revokes the active Telegram authorization when one exists. Local
// Keychain deletion and authorization-epoch invalidation are coordinated by
// the application layer only after this method succeeds.
func (a *Account) Logout(ctx context.Context) error {
	err := a.run(ctx, func(runContext context.Context) error {
		status, err := a.client.Auth().Status(runContext)
		if err != nil {
			return sanitizeRuntimeError(err)
		}
		if !status.Authorized {
			return nil
		}
		result, err := a.client.API().AuthLogOut(runContext)
		if result != nil {
			clear(result.FutureAuthToken)
		}
		if err != nil {
			return sanitizeRuntimeError(err)
		}
		return nil
	})
	return sanitizeAccountError(err)
}
