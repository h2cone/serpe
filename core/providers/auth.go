package providers

import (
	"context"
	"fmt"
	"net/http"
)

// Authenticator adds current authentication material to an outgoing request.
// Implementations must be safe for concurrent use and must not include secrets
// in returned errors.
type Authenticator interface {
	Authenticate(ctx context.Context, request *http.Request) error
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(context.Context, *http.Request) error

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, request *http.Request) error {
	return f(ctx, request)
}

// TokenSource returns a current bearer token and is safe for concurrent use.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function to TokenSource.
type TokenSourceFunc func(context.Context) (string, error)

// Token calls f.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) {
	return f(ctx)
}

type bearerAuthenticator struct {
	source TokenSource
}

// BearerAuthenticator creates a rotating bearer-token authenticator.
func BearerAuthenticator(source TokenSource) Authenticator {
	return &bearerAuthenticator{source: source}
}

func (a *bearerAuthenticator) Authenticate(ctx context.Context, request *http.Request) error {
	if a == nil || a.source == nil {
		return fmt.Errorf("bearer authenticator: token source is nil")
	}
	token, err := a.source.Token(ctx)
	if err != nil {
		return fmt.Errorf("bearer authenticator: token source failed: %w", err)
	}
	if token == "" {
		return fmt.Errorf("bearer authenticator: token source returned an empty token")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return nil
}
