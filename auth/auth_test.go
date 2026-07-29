package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/jacques/config"
	"github.com/jack-work/msauth"
)

// stubAcquirer records the exact request jacques hands to msauth. No broker,
// no Azure CLI, no network, no cache writes.
type stubAcquirer struct {
	calls  []msauth.TokenRequest
	result msauth.TokenResult
	err    error
}

func (s *stubAcquirer) Acquire(_ context.Context, request msauth.TokenRequest) (msauth.TokenResult, error) {
	s.calls = append(s.calls, request)
	return s.result, s.err
}

func withStub(t *testing.T, stub *stubAcquirer) {
	t.Helper()
	original := newAcquirer
	newAcquirer = func(config.Connection) (acquirer, error) { return stub, nil }
	t.Cleanup(func() { newAcquirer = original })
}

func validResult(token string) msauth.TokenResult {
	return msauth.TokenResult{
		AccessToken: token,
		Source:      "azure-cli",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
}

func TestSelectorReachesMsauthUnmodified(t *testing.T) {
	cases := []struct {
		name         string
		scopes       string
		wantScope    string
		wantAudience string
	}{
		{name: "default form", scopes: "https://help.kusto.windows.net/.default", wantScope: "https://help.kusto.windows.net/.default"},
		{name: "user_impersonation form", scopes: "https://kusto.kusto.windows.net/user_impersonation", wantScope: "https://kusto.kusto.windows.net/user_impersonation"},
		{name: "arbitrary scope form", scopes: "https://mycluster.centralus.kusto.windows.net/Data.Read", wantScope: "https://mycluster.centralus.kusto.windows.net/Data.Read"},
		{name: "bare resource becomes audience", scopes: "https://help.kusto.windows.net", wantAudience: "https://help.kusto.windows.net"},
		{name: "bare resource with trailing slash", scopes: "https://help.kusto.windows.net/", wantAudience: "https://help.kusto.windows.net"},
		{name: "surrounding whitespace trimmed only at the edges", scopes: "  https://help.kusto.windows.net/.default  ", wantScope: "https://help.kusto.windows.net/.default"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubAcquirer{result: validResult("stub-token")}
			withStub(t, stub)

			conn := config.Connection{Name: "telemetry", Type: "kusto", Scopes: tc.scopes, TokenProvider: "az", TenantID: "72f988bf-86f1-41af-91ab-2d7cd011db47"}
			token, err := GetToken(context.Background(), conn)
			if err != nil {
				t.Fatalf("GetToken returned error: %v", err)
			}
			if token != "stub-token" {
				t.Fatalf("token = %q, want stub-token", token)
			}
			if len(stub.calls) != 1 {
				t.Fatalf("acquire calls = %d, want 1", len(stub.calls))
			}
			got := stub.calls[0]
			if got.Scope != tc.wantScope {
				t.Errorf("scope = %q, want %q", got.Scope, tc.wantScope)
			}
			if got.Audience != tc.wantAudience {
				t.Errorf("audience = %q, want %q", got.Audience, tc.wantAudience)
			}
			if (got.Scope == "") == (got.Audience == "") {
				t.Errorf("exactly one of scope or audience must be set, got scope=%q audience=%q", got.Scope, got.Audience)
			}
			if got.Policy != msauth.PolicyAzureCLIOnly {
				t.Errorf("policy = %q, want %q", got.Policy, msauth.PolicyAzureCLIOnly)
			}
			if got.ForceRefresh {
				t.Error("ForceRefresh must stay false; msauth owns refresh skew")
			}
		})
	}
}

func TestMultiScopeStringIsHandedToMsauthForRejection(t *testing.T) {
	// jacques does not split or derive; msauth owns single-resource validation.
	stub := &stubAcquirer{result: validResult("stub-token")}
	withStub(t, stub)

	conn := config.Connection{Name: "multi", Scopes: "https://a.kusto.windows.net/.default https://b.kusto.windows.net/.default"}
	if _, err := GetToken(context.Background(), conn); err != nil {
		t.Fatalf("unexpected error from stub: %v", err)
	}
	if got := stub.calls[0].Scope; got != conn.Scopes {
		t.Fatalf("scope = %q, want the configured value verbatim %q", got, conn.Scopes)
	}
}

func TestEmptyAccessTokenIsRejectedWithStableCode(t *testing.T) {
	stub := &stubAcquirer{result: msauth.TokenResult{AccessToken: "", Source: "azure-cli", ExpiresAt: time.Now().Add(time.Hour)}}
	withStub(t, stub)

	conn := config.Connection{Name: "telemetry", Scopes: "https://help.kusto.windows.net/.default"}
	token, err := GetToken(context.Background(), conn)
	if err == nil {
		t.Fatal("expected an error for an empty access token")
	}
	if token != "" {
		t.Fatalf("token = %q, want empty", token)
	}
	var authErr *msauth.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("error is not *msauth.AuthError: %T", err)
	}
	if authErr.Code != msauth.CodeAcquisitionFailed {
		t.Errorf("code = %q, want %q", authErr.Code, msauth.CodeAcquisitionFailed)
	}
	if len(authErr.Attempts) != 1 || authErr.Attempts[0].Source != "azure-cli" {
		t.Errorf("attempts = %+v, want one azure-cli attempt", authErr.Attempts)
	}
	if strings.Contains(FormatError(err), "no token available") {
		t.Error("acquisition failure must not masquerade as the backend configuration message")
	}
}

func TestTokenProviderValues(t *testing.T) {
	for _, provider := range []string{"az", ""} {
		t.Run("accepted_"+provider, func(t *testing.T) {
			stub := &stubAcquirer{result: validResult("stub-token")}
			withStub(t, stub)
			conn := config.Connection{Name: "c", Scopes: "https://help.kusto.windows.net/.default", TokenProvider: provider}
			token, err := GetToken(context.Background(), conn)
			if err != nil {
				t.Fatalf("token_provider %q: unexpected error %v", provider, err)
			}
			if token != "stub-token" || len(stub.calls) != 1 {
				t.Fatalf("token_provider %q: token=%q calls=%d", provider, token, len(stub.calls))
			}
		})
	}

	t.Run("unknown", func(t *testing.T) {
		stub := &stubAcquirer{result: validResult("stub-token")}
		withStub(t, stub)
		conn := config.Connection{Name: "c", Scopes: "https://help.kusto.windows.net/.default", TokenProvider: "hush"}
		if _, err := GetToken(context.Background(), conn); err == nil {
			t.Fatal("expected an error for an unknown token_provider")
		} else {
			var authErr *msauth.AuthError
			if !errors.As(err, &authErr) {
				t.Fatalf("error is not *msauth.AuthError: %T", err)
			}
			if authErr.Code != msauth.CodeInvalidRequest {
				t.Errorf("code = %q, want %q", authErr.Code, msauth.CodeInvalidRequest)
			}
		}
		if len(stub.calls) != 0 {
			t.Errorf("acquire calls = %d, want 0 for an unknown provider", len(stub.calls))
		}
	})
}

func TestRuntimeConnTokenShortCircuits(t *testing.T) {
	stub := &stubAcquirer{err: errors.New("must not be called")}
	withStub(t, stub)

	conn := config.Connection{Name: "c", Scopes: "https://help.kusto.windows.net/.default", Token: "runtime-token"}
	token, err := GetToken(context.Background(), conn)
	if err != nil || token != "runtime-token" {
		t.Fatalf("token=%q err=%v, want runtime-token and no error", token, err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("acquire calls = %d, want 0 when a runtime token is present", len(stub.calls))
	}
}

func TestEmptyScopesNeedsNoToken(t *testing.T) {
	for _, scopes := range []string{"", "   "} {
		stub := &stubAcquirer{err: errors.New("must not be called")}
		withStub(t, stub)
		conn := config.Connection{Name: "local", Type: "duckdb", Scopes: scopes}
		token, err := GetToken(context.Background(), conn)
		if err != nil || token != "" {
			t.Fatalf("scopes %q: token=%q err=%v, want empty token and no error", scopes, token, err)
		}
		if len(stub.calls) != 0 {
			t.Errorf("scopes %q: acquire calls = %d, want 0", scopes, len(stub.calls))
		}
	}
}

func TestFormatErrorSurfacesCodeAndAttempts(t *testing.T) {
	stub := &stubAcquirer{err: &msauth.AuthError{
		Code:    msauth.CodeAcquisitionFailed,
		Message: "no configured credential source could satisfy the token request",
		Attempts: []msauth.Attempt{
			{Source: "azure-cli", Code: msauth.CodeAzureCLIFailed, Message: "Azure CLI command failed: exit status 1: Please run 'az login'"},
		},
	}}
	withStub(t, stub)

	conn := config.Connection{Name: "telemetry", Scopes: "https://help.kusto.windows.net/.default"}
	_, err := GetToken(context.Background(), conn)
	if err == nil {
		t.Fatal("expected an acquisition error")
	}
	rendered := FormatError(err)
	for _, want := range []string{"acquisition_failed", "azure-cli", "azure_cli_failed", "az login"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered error %q missing %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "configure tenant_id") {
		t.Error("acquisition failure must not be rendered as a configuration error")
	}
}

func TestNonAuthErrorGetsStableInternalCode(t *testing.T) {
	stub := &stubAcquirer{err: errors.New("boom")}
	withStub(t, stub)

	_, err := GetToken(context.Background(), config.Connection{Name: "c", Scopes: "https://help.kusto.windows.net/.default"})
	var authErr *msauth.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("error is not *msauth.AuthError: %T", err)
	}
	if authErr.Code != msauth.CodeInternal {
		t.Errorf("code = %q, want %q", authErr.Code, msauth.CodeInternal)
	}
}

func TestProductionAcquirerUsesAzureCLIClientAndConfiguredTenant(t *testing.T) {
	// Constructs the provider only; no acquisition happens, so this stays hermetic.
	got, err := newAcquirer(config.Connection{TenantID: "72f988bf-86f1-41af-91ab-2d7cd011db47"})
	if err != nil {
		t.Fatalf("newAcquirer: %v", err)
	}
	provider, ok := got.(*msauth.Provider)
	if !ok {
		t.Fatalf("acquirer type = %T, want *msauth.Provider", got)
	}
	if provider.ClientID != msauth.AzureCLIClientID {
		t.Errorf("client id = %q, want the azure-cli client", provider.ClientID)
	}
	if provider.TenantID != "72f988bf-86f1-41af-91ab-2d7cd011db47" {
		t.Errorf("tenant id = %q, want the configured tenant", provider.TenantID)
	}
}
