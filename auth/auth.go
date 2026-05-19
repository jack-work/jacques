// Package auth handles token acquisition and caching for connections that
// require bearer tokens.
//
// Flow:
//  1. Check hush-encrypted cache for a valid (non-expired) access token → use it.
//  2. Cache miss or expired → acquire via `az account get-access-token`.
//     az manages its own refresh tokens and browser-based auth internally,
//     which satisfies Conditional Access policies.
//  3. Cache the fresh token encrypted with hush.
//
// Connections with no scopes configured are assumed not to need a token;
// GetToken returns "" with no error.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	hushclient "github.com/jack-work/hush/client"
	"github.com/jokellih/jacques/config"
	"github.com/jokellih/jacques/logging"
)

const expiryBuffer = 2 * time.Minute

// ---------------------------------------------------------------------------
// cache (flat TOML, sensitive values hush-encrypted)
// ---------------------------------------------------------------------------

type tokenCache map[string]string

func cachePath() string {
	return filepath.Join(config.Dir(), "token-cache.toml")
}

func loadCache() tokenCache {
	raw, err := os.ReadFile(cachePath())
	if err != nil {
		return make(tokenCache)
	}
	var c tokenCache
	if err := toml.Unmarshal(raw, &c); err != nil {
		return make(tokenCache)
	}
	return c
}

func saveCache(c tokenCache) error {
	if err := config.EnsureDir(); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return os.WriteFile(cachePath(), buf.Bytes(), 0o600)
}

// ---------------------------------------------------------------------------
// public API
// ---------------------------------------------------------------------------

// GetToken returns a valid bearer token for the connection.
func GetToken(ctx context.Context, conn config.Connection) (string, error) {
	if conn.Token != "" {
		return conn.Token, nil
	}
	if conn.Scopes == "" {
		return "", nil
	}

	hush, err := hushclient.New()
	if err != nil {
		return "", fmt.Errorf("hush agent required for token cache: %w\n  start it with: hush up -d", err)
	}

	cache := loadCache()
	prefix := conn.Name + "_"

	if token, ok := tryCachedToken(ctx, hush, cache, prefix); ok {
		return token, nil
	}

	reason := tokenMissReason(cache, prefix)

	switch conn.TokenProvider {
	case "az", "":
		return acquireViaAz(ctx, conn, hush, cache, prefix, reason)
	default:
		return "", fmt.Errorf("unknown token_provider %q (supported: az)", conn.TokenProvider)
	}
}

func acquireViaAz(ctx context.Context, conn config.Connection, hush *hushclient.Client, cache tokenCache, prefix, reason string) (string, error) {
	fmt.Fprintf(os.Stderr, "\x1b[38;5;241m\u25cb token %s, acquiring via az cli\x1b[0m\n", reason)
	logging.Info(ctx, "acquiring token via az cli",
		logging.String("connection", conn.Name),
		logging.String("scopes", conn.Scopes),
	)

	resource := resourceFromScopes(conn.Scopes)
	token, expiresOn, err := azGetToken(resource)
	if err != nil {
		return "", err
	}

	if err := persistToken(hush, cache, prefix, token, expiresOn); err != nil {
		logging.Warn(ctx, "failed to cache token", logging.String("error", err.Error()))
	}

	return token, nil
}

// ---------------------------------------------------------------------------
// cache helpers
// ---------------------------------------------------------------------------

func tryCachedToken(ctx context.Context, hush *hushclient.Client, cache tokenCache, prefix string) (string, bool) {
	encToken, ok := cache[prefix+"access_token"]
	if !ok {
		return "", false
	}
	expiryStr, ok := cache[prefix+"expires_on"]
	if !ok {
		return "", false
	}
	expiry, err := time.Parse(time.RFC3339, expiryStr)
	if err != nil || time.Now().After(expiry.Add(-expiryBuffer)) {
		return "", false
	}

	dec, err := hush.Decrypt(map[string]string{"t": encToken})
	if err != nil {
		logging.Warn(ctx, "decrypt cached token failed, re-acquiring",
			logging.String("error", err.Error()))
		return "", false
	}

	remaining := time.Until(expiry).Truncate(time.Second)
	logging.Info(ctx, "using cached token",
		logging.String("expires", expiry.Format(time.RFC3339)),
	)
	fmt.Fprintf(os.Stderr, "\x1b[38;5;241m\u2713 token cached (expires in %s)\x1b[0m\n", remaining)
	return dec["t"], true
}

func persistToken(hush *hushclient.Client, cache tokenCache, prefix, token string, expiresOn time.Time) error {
	enc, err := hush.Encrypt(map[string]string{"t": token})
	if err != nil {
		return fmt.Errorf("encrypt token: %w", err)
	}
	cache[prefix+"access_token"] = enc["t"]
	cache[prefix+"expires_on"] = expiresOn.Format(time.RFC3339)
	return saveCache(cache)
}

// ---------------------------------------------------------------------------
// az cli token acquisition
// ---------------------------------------------------------------------------

type azTokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpiresOn   string `json:"expiresOn"`
}

func azGetToken(resource string) (string, time.Time, error) {
	cmd := exec.Command("az", "account", "get-access-token",
		"--resource", resource, "--output", "json")
	out, err := cmd.Output()
	if err != nil {
		return "", time.Time{}, fmt.Errorf(
			"az account get-access-token failed: %w\n  run 'az login' first", err)
	}

	var resp azTokenResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", time.Time{}, fmt.Errorf("parse az token response: %w", err)
	}

	expiry := parseExpiry(resp.ExpiresOn)
	return resp.AccessToken, expiry, nil
}

// resourceFromScopes extracts the resource URI from an OAuth2 scope string.
// "https://help.kusto.windows.net/.default" → "https://help.kusto.windows.net"
func resourceFromScopes(scopes string) string {
	s := strings.Split(scopes, " ")[0]
	s = strings.TrimSuffix(s, "/.default")
	s = strings.TrimSuffix(s, "/user_impersonation")
	return s
}

func tokenMissReason(cache tokenCache, prefix string) string {
	if _, ok := cache[prefix+"access_token"]; !ok {
		return "not cached"
	}
	return "expired"
}

func parseExpiry(s string) time.Time {
	// az cli returns expiresOn in local time without timezone
	for _, f := range []string{
		"2006-01-02 15:04:05.000000",
		"2006-01-02T15:04:05.000000",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			return t
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Now().Add(1 * time.Hour)
}
