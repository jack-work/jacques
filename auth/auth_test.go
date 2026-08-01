package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	if strings.Contains(msauth.FormatError("token acquisition failed", err), "no token available") {
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

func TestAcquisitionFailureRendersThroughTheFoundation(t *testing.T) {
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
	rendered := msauth.FormatError("token acquisition failed", err)
	for _, want := range []string{"token acquisition failed", "acquisition_failed", "azure-cli", "azure_cli_failed", "az login"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered error %q missing %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "configure tenant_id") {
		t.Error("acquisition failure must not be rendered as a configuration error")
	}
	// The actionable line is the whole reason jacques stopped rendering its
	// own: a cold Azure CLI must be told to log in, not left to guess.
	if hint := msauth.Hint(err); hint != "Run: az login" {
		t.Errorf("hint = %q, want %q", hint, "Run: az login")
	}
}

// TestBrokerFaultHintsAtDoctor pins the case the plain renderer cannot serve:
// a signed-in user whose Az.Accounts is broken must be sent to msauth doctor,
// not to a login that was never the problem.
func TestBrokerFaultHintsAtDoctor(t *testing.T) {
	err := &msauth.AuthError{
		Code:     msauth.CodeAcquisitionFailed,
		Message:  "no configured credential source could satisfy the token request",
		Attempts: []msauth.Attempt{{Source: "wam", Code: msauth.CodeBrokerUnavailable, Message: "broker assembly not found"}},
	}
	if hint := msauth.Hint(err); hint != "Run: msauth doctor" {
		t.Errorf("hint = %q, want %q", hint, "Run: msauth doctor")
	}
	if hint := msauth.Hint(errors.New("boom")); hint != "" {
		t.Errorf("hint for a foreign error = %q, want empty", hint)
	}
}

// TestNoLocalAuthErrorRenderer is the tripwire for the duplication this change
// removed. jacques rendered *msauth.AuthError itself for as long as it had a
// FormatError of its own; a one-line wrapper is how the copy would come back,
// so the check is on the declaration and on the copied attempt layout rather
// than on behavior.
func TestNoLocalAuthErrorRenderer(t *testing.T) {
	// Built at runtime so this test file does not match itself.
	declaration := "func " + "FormatError("
	hintDecl := "func " + "Hint("
	attemptLayout := "\\n" + "  %s [%s]: %s"

	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "testdata", "scratch", "tmp":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		for _, needle := range []string{declaration, hintDecl, attemptLayout} {
			if strings.Contains(text, needle) {
				t.Errorf("%s declares or re-implements the foundation's auth renderer (%q); call msauth.FormatError and msauth.Hint directly", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestPolicyIsPinnedExplicitly guards the pin at the request, not the default.
// jacques has no broker path; inheriting msauth's per-client default would let
// a change there route a jacques acquisition through WAM without a code change
// here, so the value must be set on every request jacques sends.
func TestPolicyIsPinnedExplicitly(t *testing.T) {
	stub := &stubAcquirer{result: validResult("stub-token")}
	withStub(t, stub)

	conn := config.Connection{Name: "telemetry", Scopes: "https://help.kusto.windows.net/.default"}
	if _, err := GetToken(context.Background(), conn); err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	got := stub.calls[0].Policy
	if got == "" {
		t.Fatal("policy is empty: jacques must pin it rather than inherit msauth's default")
	}
	if got != msauth.PolicyAzureCLIOnly {
		t.Fatalf("policy = %q, want %q", got, msauth.PolicyAzureCLIOnly)
	}
	for _, forbidden := range []msauth.Policy{msauth.PolicyWAMFirst, msauth.PolicyWAMOnly} {
		if got == forbidden {
			t.Fatalf("policy = %q: jacques has no broker path", got)
		}
	}
}

func TestNamespaceForConfigDir(t *testing.T) {
	base := t.TempDir()
	one := filepath.Join(base, "login-a")
	two := filepath.Join(base, "login-b")

	empty := namespaceForConfigDir("")
	if empty != "" {
		t.Fatalf("absent AZURE_CONFIG_DIR yielded %q, want the empty shared-default namespace", empty)
	}
	if got := namespaceForConfigDir("   "); got != "" {
		t.Errorf("blank AZURE_CONFIG_DIR yielded %q, want empty", got)
	}

	canonical := namespaceForConfigDir(one)
	if canonical == "" {
		t.Fatal("a present AZURE_CONFIG_DIR must not yield the shared-default namespace")
	}
	if len(canonical) > 64 {
		t.Errorf("namespace is %d bytes: %q", len(canonical), canonical)
	}
	for _, r := range canonical {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("namespace %q contains a control character; msauth rejects those", canonical)
		}
	}

	// Spellings of ONE directory that must collapse to ONE namespace. Each of
	// these fooled a naive os.Getenv passthrough.
	spellings := []struct {
		name  string
		value string
	}{
		{name: "identical", value: one},
		{name: "trailing separator", value: one + string(filepath.Separator)},
		{name: "surrounding whitespace", value: "  " + one + "  "},
		{name: "dot segment", value: filepath.Join(one, ".")},
		{name: "parent then back", value: filepath.Join(one, "nested", "..")},
		{name: "forward slashes", value: filepath.ToSlash(one)},
	}
	if runtime.GOOS == "windows" {
		spellings = append(spellings,
			struct {
				name  string
				value string
			}{name: "upper case", value: strings.ToUpper(one)},
			struct {
				name  string
				value string
			}{name: "backslashes", value: strings.ReplaceAll(filepath.ToSlash(one), "/", "\\")},
		)
	}
	for _, tc := range spellings {
		t.Run(tc.name, func(t *testing.T) {
			if got := namespaceForConfigDir(tc.value); got != canonical {
				t.Errorf("namespace(%q) = %q, want %q -- one directory must be one namespace", tc.value, got, canonical)
			}
		})
	}

	// Two directories must never collide, or one login's token is served to
	// the other inside the validity window.
	if other := namespaceForConfigDir(two); other == canonical {
		t.Fatalf("distinct directories %q and %q share namespace %q", one, two, canonical)
	}
	if other := namespaceForConfigDir(two); other == "" {
		t.Fatal("a second directory collapsed to the shared-default namespace")
	}
	// A sibling whose name is a prefix of another must not collide either.
	if got := namespaceForConfigDir(one + "-extra"); got == canonical {
		t.Errorf("prefix sibling %q shares namespace with %q", one+"-extra", one)
	}
}

// TestCacheNamespaceReachesMsauth proves the derivation is not merely correct
// in isolation: the value must actually land in the TokenRequest, and it must
// be read at request time so a .env loaded into the process is honored.
func TestCacheNamespaceReachesMsauth(t *testing.T) {
	dir := t.TempDir()

	t.Run("absent stays on the shared default", func(t *testing.T) {
		t.Setenv("AZURE_CONFIG_DIR", "")
		stub := &stubAcquirer{result: validResult("stub-token")}
		withStub(t, stub)
		if _, err := GetToken(context.Background(), config.Connection{Name: "c", Scopes: "https://help.kusto.windows.net/.default"}); err != nil {
			t.Fatalf("GetToken: %v", err)
		}
		if got := stub.calls[0].CacheNamespace; got != "" {
			t.Fatalf("CacheNamespace = %q, want empty so warm cache entries are not orphaned", got)
		}
	})

	t.Run("pinned login gets its own namespace", func(t *testing.T) {
		t.Setenv("AZURE_CONFIG_DIR", dir)
		stub := &stubAcquirer{result: validResult("stub-token")}
		withStub(t, stub)
		if _, err := GetToken(context.Background(), config.Connection{Name: "c", Scopes: "https://help.kusto.windows.net/.default"}); err != nil {
			t.Fatalf("GetToken: %v", err)
		}
		if got, want := stub.calls[0].CacheNamespace, namespaceForConfigDir(dir); got != want {
			t.Fatalf("CacheNamespace = %q, want %q", got, want)
		}
	})

	t.Run("two connections under one login share it", func(t *testing.T) {
		t.Setenv("AZURE_CONFIG_DIR", dir)
		stub := &stubAcquirer{result: validResult("stub-token")}
		withStub(t, stub)
		for _, name := range []string{"telemetry", "incidents"} {
			if _, err := GetToken(context.Background(), config.Connection{Name: name, Scopes: "https://help.kusto.windows.net/.default"}); err != nil {
				t.Fatalf("GetToken(%s): %v", name, err)
			}
		}
		if len(stub.calls) != 2 {
			t.Fatalf("acquire calls = %d, want 2", len(stub.calls))
		}
		if stub.calls[0].CacheNamespace != stub.calls[1].CacheNamespace {
			t.Fatalf("namespaces %q and %q differ; the namespace follows the login, not the connection",
				stub.calls[0].CacheNamespace, stub.calls[1].CacheNamespace)
		}
	})

	t.Run("one connection under two logins does not", func(t *testing.T) {
		conn := config.Connection{Name: "telemetry", Scopes: "https://help.kusto.windows.net/.default"}
		seen := make([]string, 0, 2)
		for _, configDir := range []string{dir, t.TempDir()} {
			t.Setenv("AZURE_CONFIG_DIR", configDir)
			stub := &stubAcquirer{result: validResult("stub-token")}
			withStub(t, stub)
			if _, err := GetToken(context.Background(), conn); err != nil {
				t.Fatalf("GetToken: %v", err)
			}
			seen = append(seen, stub.calls[0].CacheNamespace)
		}
		if seen[0] == seen[1] {
			t.Fatalf("one connection under two logins shared namespace %q", seen[0])
		}
	})
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
