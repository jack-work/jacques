// Package auth adapts jacques connections to the shared msauth foundation.
//
// jacques prefers the platform broker and falls back to the Azure CLI: ADX works
// from the current
// Azure CLI identity, and inside WSL from the Windows broker msauth bridges to,
// so every request uses msauth's wam-first policy: broker where one exists,
// Azure CLI everywhere else.
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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
// per-source attempts. Render them with msauth.FormatError and msauth.Hint;
// jacques keeps no renderer of its own.
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
	// Pinned explicitly, not inherited, so a day when msauth's per-client
	// default moves cannot silently change which credential a query runs under.
	//
	// This was PolicyAzureCLIOnly, on the grounds that "jacques has no broker
	// path at all". That is no longer true. msauth now treats a broker as a
	// PLATFORM facility: WAM on Windows, and inside WSL the Windows host's
	// broker reached over interop. On this box that is the only path that
	// works at all -- the account's passkey is Windows Hello, a platform
	// authenticator Linux cannot drive, and a WSL VM has no USB for a security
	// key, so an azure-cli-only jacques is unauthenticatable by construction.
	//
	// WAMFirst rather than WAMOnly: the broker is preferred where it exists,
	// and a machine with a warm `az login` and no broker keeps working exactly
	// as before. Both orderings are honest here; only this one degrades.
	request.Policy = msauth.PolicyWAMFirst
	request.CacheNamespace = cacheNamespace()

	fmt.Fprintf(os.Stderr, "\x1b[38;5;241m○ acquiring token via msauth (wam-first)\x1b[0m\n")
	logging.Info(ctx, "acquiring token via msauth",
		logging.String("connection", conn.Name),
		logging.String("selector", conn.Scopes),
		logging.String("policy", string(msauth.PolicyWAMFirst)),
		logging.String("cache_namespace", request.CacheNamespace),
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

// cacheNamespace derives msauth's identity discriminator from the effective
// AZURE_CONFIG_DIR, read at request time so it reflects any value a working
// directory's .env has already loaded.
//
// msauth keys its cache by client, tenant, and scope, which cannot tell two
// signed-in Azure CLI identities apart. jacques can pin a different Azure CLI
// login per working directory through AZURE_CONFIG_DIR, so it must supply the
// discriminator itself or one directory's token can be served to another
// inside the validity window.
//
// The namespace follows the login, not the connection: two connections under
// one login share a cache entry, and one connection under two logins does not.
func cacheNamespace() string {
	return namespaceForConfigDir(os.Getenv("AZURE_CONFIG_DIR"))
}

// namespaceForConfigDir normalizes one AZURE_CONFIG_DIR value into a stable,
// bounded namespace.
//
// An absent or blank value yields the EMPTY namespace deliberately: that is
// msauth's shared default, and any other choice would orphan every warm cache
// entry already on the machine.
//
// A present value is made absolute, cleaned, and case-folded on Windows so two
// spellings of one directory collapse to one namespace, then hashed with
// SHA-256 and truncated. Hashing keeps the result well inside msauth's length
// limit, free of control characters, and independent of how long or how deeply
// nested the directory is; the namespace is an opaque discriminator, never a
// path the user reads back.
func namespaceForConfigDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		dir = strings.ToLower(dir)
	}
	sum := sha256.Sum256([]byte(dir))
	return "azure-config-dir-" + hex.EncodeToString(sum[:])[:32]
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
