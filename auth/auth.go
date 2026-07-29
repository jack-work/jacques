// Package auth adapts jacques connections to the shared msauth foundation.
//
// jacques has no broker (WAM) path: Azure Data Explorer works from the current
// Azure CLI identity, so every request uses msauth's azure-cli-only policy.
// msauth owns acquisition, caching, refresh skew, sanitized diagnostics, and
// stable error codes; jacques owns only connection semantics.
//
// The connection's configured `scopes` value in config.hcl is the single source
// of truth for the token selector and is handed to msauth unmodified. jacques
// never derives a resource of its own.
//
// Connections with no scopes configured are assumed not to need a token;
// GetToken returns "" with no error.
package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jack-work/jacques/config"
	"github.com/jack-work/jacques/logging"
	"github.com/jack-work/msauth"
)

// acquirer is the msauth seam. Production always uses *msauth.Provider; tests
// substitute a stub so no broker, Azure CLI, or network is required.
type acquirer interface {
	Acquire(ctx context.Context, request msauth.TokenRequest) (msauth.TokenResult, error)
}

// newAcquirer is overridden only by tests.
var newAcquirer = func(conn config.Connection) (acquirer, error) {
	provider, err := msauth.NewForClient("azure-cli")
	if err != nil {
		return nil, err
	}
	if tenant := strings.TrimSpace(conn.TenantID); tenant != "" {
		provider.TenantID = tenant
	}
	return provider, nil
}

// GetToken returns a bearer token for the connection.
//
// Errors are *msauth.AuthError values carrying a stable code and sanitized
// per-source attempts. Use FormatError to render them.
func GetToken(ctx context.Context, conn config.Connection) (string, error) {
	if conn.Token != "" {
		return conn.Token, nil
	}
	if strings.TrimSpace(conn.Scopes) == "" {
		return "", nil
	}

	switch conn.TokenProvider {
	case "az", "":
	default:
		return "", &msauth.AuthError{
			Code:    msauth.CodeInvalidRequest,
			Message: fmt.Sprintf("connection %q: unknown token_provider %q (supported: az)", conn.Name, conn.TokenProvider),
		}
	}

	request := selectorFor(conn.Scopes)
	request.Policy = msauth.PolicyAzureCLIOnly

	fmt.Fprintf(os.Stderr, "\x1b[38;5;241m○ acquiring token via msauth (azure-cli-only)\x1b[0m\n")
	logging.Info(ctx, "acquiring token via msauth",
		logging.String("connection", conn.Name),
		logging.String("selector", conn.Scopes),
		logging.String("policy", string(msauth.PolicyAzureCLIOnly)),
		logging.String("config_tenant_id", conn.TenantID),
		logging.String("config_client_id", conn.ClientID),
	)

	provider, err := newAcquirer(conn)
	if err != nil {
		return "", wrapNonAuthError(conn, err)
	}

	result, err := provider.Acquire(ctx, request)
	if err != nil {
		return "", wrapNonAuthError(conn, err)
	}
	if result.AccessToken == "" {
		return "", &msauth.AuthError{
			Code:    msauth.CodeAcquisitionFailed,
			Message: fmt.Sprintf("connection %q: credential source returned an empty access token", conn.Name),
			Attempts: []msauth.Attempt{{
				Source:  sourceOrUnknown(result.Source),
				Code:    msauth.CodeAcquisitionFailed,
				Message: "empty access token",
			}},
		}
	}

	logging.Info(ctx, "token acquired",
		logging.String("connection", conn.Name),
		logging.String("source", result.Source),
		logging.String("expires_at", result.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")),
	)
	return result.AccessToken, nil
}

// selectorFor turns the configured scopes value into exactly one msauth
// selector. A value with a path segment is a scope and is passed through
// byte-for-byte (including the /.default and /user_impersonation forms). A bare
// resource identifier is passed as an audience, which msauth normalizes to its
// resource-wide /.default scope. jacques strips nothing else.
func selectorFor(scopes string) msauth.TokenRequest {
	value := strings.TrimSpace(scopes)
	rest := value
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if strings.Contains(strings.TrimSuffix(rest, "/"), "/") {
		return msauth.TokenRequest{Scope: value}
	}
	return msauth.TokenRequest{Audience: strings.TrimSuffix(value, "/")}
}

func sourceOrUnknown(source string) string {
	if source == "" {
		return "unknown"
	}
	return source
}

// wrapNonAuthError preserves msauth errors verbatim and gives anything else a
// stable code so callers never see an untyped acquisition failure.
func wrapNonAuthError(conn config.Connection, err error) error {
	var authErr *msauth.AuthError
	if errors.As(err, &authErr) {
		return err
	}
	return &msauth.AuthError{
		Code:    msauth.CodeInternal,
		Message: fmt.Sprintf("connection %q: %v", conn.Name, err),
	}
}

// FormatError renders an acquisition failure with its stable msauth code and
// sanitized per-source attempts, so a token failure is never mistaken for a
// configuration problem.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	var authErr *msauth.AuthError
	if !errors.As(err, &authErr) {
		return err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "token acquisition failed [%s]: %s", authErr.Code, authErr.Message)
	for _, attempt := range authErr.Attempts {
		fmt.Fprintf(&b, "\n  %s [%s]: %s", attempt.Source, attempt.Code, attempt.Message)
	}
	return b.String()
}
