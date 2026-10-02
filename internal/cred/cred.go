// Package cred owns authentication state: the device-login flow, credential
// persistence, and access-token refresh.
package cred

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"codex-workbuddyai-setup/internal/paths"
)

// Credentials is the on-disk credential layout written to credentials.json.
type Credentials struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	TokenType        string `json:"tokenType"`
	Scope            string `json:"scope"`
	SessionState     string `json:"sessionState"`
	Domain           string `json:"domain"`
	UID              string `json:"uid"`
	ExpiresIn        string `json:"expiresIn"`
	ExpiresAt        any    `json:"expiresAt"`
	RefreshExpiresIn string `json:"refreshExpiresIn"`
	RefreshExpiresAt any    `json:"refreshExpiresAt"`
	ObtainedAt       string `json:"obtainedAt"`
	Source           string `json:"source"`
}

// ErrNotLoggedIn is returned when no usable credential exists on disk.
var ErrNotLoggedIn = errors.New("no credentials found; run `wbai login`")

// Load reads credentials from disk. It returns ErrNotLoggedIn when the file is
// missing, unparsable, or carries no access token.
func Load() (*Credentials, error) {
	path, err := paths.Credentials()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotLoggedIn
		}
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if c.AccessToken == "" {
		return nil, ErrNotLoggedIn
	}
	if c.UID == "" {
		c.UID = jwtSub(c.AccessToken)
	}
	return &c, nil
}

// Save writes credentials to disk with 0600 permissions. Unlike the original
// implementation every failure is surfaced instead of being swallowed.
func Save(c *Credentials) error {
	path, err := paths.Credentials()
	if err != nil {
		return err
	}
	if _, err := paths.EnsureDir(); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}
	pretty, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	if err := os.WriteFile(path, pretty, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

// FromToken converts a fresh token payload into stored credentials.
func FromToken(access, refresh, tokenType, scope, session, domain string) *Credentials {
	now := time.Now().UTC()
	return &Credentials{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    tokenType,
		Scope:        scope,
		SessionState: session,
		Domain:       domain,
		UID:          jwtSub(access),
		ObtainedAt:   now.Format(time.RFC3339Nano),
		Source:       "workbuddy-login-poll",
	}
}

// Domain returns the API domain, falling back to the compiled default.
func (c *Credentials) DomainOr(defaultDomain string) string {
	if c != nil && c.Domain != "" {
		return c.Domain
	}
	return defaultDomain
}

// jwtSub extracts the "sub" claim from a JWT payload; empty when unavailable.
func jwtSub(token string) string {
	parts := splitToken(token)
	if len(parts) < 2 {
		return ""
	}
	seg := parts[1]
	if pad := len(seg) % 4; pad != 0 {
		seg += "===="[:4-pad]
	}
	decoded, err := base64.URLEncoding.DecodeString(replacer(seg))
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(decoded, &claims) != nil {
		return ""
	}
	return claims.Sub
}

func splitToken(token string) []string {
	var out []string
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			out = append(out, token[start:i])
			start = i + 1
		}
	}
	return append(out, token[start:])
}

func replacer(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == '-' {
			b[i] = '+'
		} else if b[i] == '_' {
			b[i] = '/'
		}
	}
	return string(b)
}
