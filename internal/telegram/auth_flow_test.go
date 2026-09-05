package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

func TestPhoneFlowRequestsPasswordOnlyWhenTelegramRequiresIt(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "code_only", true: "password_required"}[required], func(t *testing.T) {
			prompt := &authPromptStub{phone: []byte("+9996621234"), code: []byte("22222")}
			err := auth.NewFlow(interactiveAuthenticator{prompt: prompt}, auth.SendCodeOptions{}).Run(context.Background(), authFlowClient{passwordRequired: required})
			if !required {
				if err != nil || prompt.passwordCalls != 0 {
					t.Fatal("code-only flow requested password", err)
				}
			} else {
				if prompt.passwordCalls != 1 || !errors.Is(sanitizeAccountError(sanitizeAuthenticationError(err)), ErrPasswordRequired) {
					t.Fatal("missing requested password lost its diagnostic", err)
				}
			}
		})
	}
}

type authFlowClient struct{ passwordRequired bool }

func (authFlowClient) SendCode(context.Context, string, auth.SendCodeOptions) (tg.AuthSentCodeClass, error) {
	return &tg.AuthSentCode{PhoneCodeHash: "synthetic-code-hash"}, nil
}
func (c authFlowClient) SignIn(context.Context, string, string, string) (*tg.AuthAuthorization, error) {
	if c.passwordRequired {
		return nil, auth.ErrPasswordAuthNeeded
	}
	return &tg.AuthAuthorization{}, nil
}
func (authFlowClient) Password(context.Context, string) (*tg.AuthAuthorization, error) {
	return nil, errors.New("unexpected plaintext password path")
}
func (authFlowClient) PasswordWith(ctx context.Context, hash auth.PasswordHashFunc) (*tg.AuthAuthorization, error) {
	_, err := hash(ctx, nil)
	return nil, err
}
func (authFlowClient) SignUp(context.Context, auth.SignUp) (*tg.AuthAuthorization, error) {
	return nil, errors.New("unexpected sign-up")
}
