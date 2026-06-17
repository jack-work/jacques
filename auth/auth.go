// Package auth handles token acquisition for connections that require bearer
// tokens. Tokens are acquired via `az account get-access-token`; az manages
// its own refresh tokens and browser-based auth internally.
//
// Connections with no scopes configured are assumed not to need a token;
// GetToken returns "" with no error.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jack-work/jacques/config"
	"github.com/jack-work/jacques/logging"
)

// GetToken returns a valid bearer token for the connection.
func GetToken(ctx context.Context, conn config.Connection) (string, error) {
	if conn.Token != "" {
		return conn.Token, nil
	}
	if conn.Scopes == "" {
		return "", nil
	}

	switch conn.TokenProvider {
	case "az", "":
		return acquireViaAz(ctx, conn)
	default:
		return "", fmt.Errorf("unknown token_provider %q (supported: az)", conn.TokenProvider)
	}
}

func acquireViaAz(ctx context.Context, conn config.Connection) (string, error) {
	fmt.Fprintf(os.Stderr, "\x1b[38;5;241m○ acquiring token via az cli\x1b[0m\n")
	logging.Info(ctx, "acquiring token via az cli",
		logging.String("connection", conn.Name),
		logging.String("scopes", conn.Scopes),
	)

	resource := resourceFromScopes(conn.Scopes)
	token, _, err := azGetToken(resource)
	if err != nil {
		return "", err
	}

	return token, nil
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
// "https://help.kusto.windows.net/.default" -> "https://help.kusto.windows.net"
func resourceFromScopes(scopes string) string {
	s := strings.Split(scopes, " ")[0]
	s = strings.TrimSuffix(s, "/.default")
	s = strings.TrimSuffix(s, "/user_impersonation")
	return s
}

func parseExpiry(s string) time.Time {
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
