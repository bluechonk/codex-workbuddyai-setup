package cred

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is used when credentials carry no explicit domain.
	DefaultBaseURL = "https://www.workbuddy.ai"
	// PlatformQS identifies this client to the WorkBuddyAI plugin endpoints.
	PlatformQS = "platform=workbuddy"

	loginWindow    = 5 * time.Minute
	pollInterval   = 5 * time.Second
	maxLoginRounds = 2
	httpTimeout    = 30 * time.Second
)

// envelope is the shared API response wrapper.
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type loginState struct {
	State   string `json:"state"`
	AuthURL string `json:"authUrl"`
}

// Token is the payload returned once the user authorizes in the browser.
type Token struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	TokenType        string `json:"tokenType"`
	Scope            string `json:"scope"`
	SessionState     string `json:"sessionState"`
	Domain           string `json:"domain"`
	ExpiresIn        any    `json:"expiresIn"`
	RefreshExpiresIn any    `json:"refreshExpiresIn"`
}

var client = &http.Client{Timeout: httpTimeout}

// anonHeaders mark a request as unauthenticated for the plugin endpoints.
func anonHeaders() map[string]string {
	return map[string]string{
		"X-No-Authorization":   "true",
		"X-No-User-Id":         "true",
		"X-No-Enterprise-Id":   "true",
		"X-No-Department-Info": "true",
	}
}

// Login runs the interactive browser login and persists the result.
// It returns the stored credentials and the path they were written to.
func Login(baseURL string) (*Credentials, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	data, err := loginFlow(baseURL)
	if err != nil {
		return nil, err
	}
	c := FromToken(
		data.AccessToken,
		data.RefreshToken,
		data.TokenType,
		data.Scope,
		data.SessionState,
		data.Domain,
	)
	if c.Domain == "" {
		c.Domain = hostOf(baseURL)
	}
	c.ExpiresIn = anyToString(data.ExpiresIn)
	c.RefreshExpiresIn = anyToString(data.RefreshExpiresIn)
	now := time.Now().UTC()
	c.ExpiresAt = unixAfter(now, data.ExpiresIn)
	c.RefreshExpiresAt = unixAfter(now, data.RefreshExpiresIn)

	if err := Save(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Refresh exchanges the stored refresh token for a new access token.
// A failure here is not fatal: callers fall back to an interactive login.
func Refresh(c *Credentials) (*Credentials, error) {
	if c == nil || c.RefreshToken == "" {
		return nil, errors.New("no refresh token available")
	}
	base := DefaultBaseURL
	if c.Domain != "" {
		base = "https://" + c.Domain
	}
	body := fmt.Sprintf(`{"refreshToken":%q}`, c.RefreshToken)
	req, err := http.NewRequest(http.MethodPost, base+"/v2/plugin/auth/refresh?"+PlatformQS, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range anonHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed: HTTP %d", resp.StatusCode)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("refresh: invalid JSON: %w", err)
	}
	if env.Code != 0 || len(env.Data) == 0 {
		return nil, fmt.Errorf("refresh failed: code=%d msg=%s", env.Code, env.Msg)
	}
	var t Token
	if err := json.Unmarshal(env.Data, &t); err != nil {
		return nil, fmt.Errorf("refresh: parse token: %w", err)
	}
	if t.AccessToken == "" {
		return nil, errors.New("refresh returned an empty access token")
	}

	next := FromToken(
		t.AccessToken,
		orString(t.RefreshToken, c.RefreshToken),
		orString(t.TokenType, c.TokenType),
		orString(t.Scope, c.Scope),
		orString(t.SessionState, c.SessionState),
		orString(t.Domain, c.Domain),
	)
	now := time.Now().UTC()
	next.ExpiresIn = anyToString(t.ExpiresIn)
	next.RefreshExpiresIn = anyToString(t.RefreshExpiresIn)
	next.ExpiresAt = unixAfter(now, t.ExpiresIn)
	next.RefreshExpiresAt = unixAfter(now, t.RefreshExpiresIn)
	if next.UID == "" {
		next.UID = c.UID
	}
	if err := Save(next); err != nil {
		return nil, err
	}
	return next, nil
}

// loginFlow retries the whole create-link/poll cycle up to maxLoginRounds.
func loginFlow(baseURL string) (*Token, error) {
	for round := 1; round <= maxLoginRounds; round++ {
		state, err := createLoginState(baseURL)
		if err != nil {
			return nil, err
		}
		authURL := state.AuthURL
		if authURL == "" {
			authURL = baseURL + "/login?" + PlatformQS + "&state=" + urlEscape(state.State)
		}
		fmt.Println()
		fmt.Printf("=== Login attempt %d/%d ===\n", round, maxLoginRounds)
		fmt.Printf("Login URL: %s\n", authURL)
		fmt.Printf("Waiting for authorization (%d minute(s))...\n", int(loginWindow.Minutes()))

		openBrowser(authURL)

		if data := pollToken(baseURL, state.State); data != nil {
			return data, nil
		}
		fmt.Printf("Login window expired (attempt %d/%d).\n", round, maxLoginRounds)
	}
	return nil, fmt.Errorf("no login detected after %d attempt(s)", maxLoginRounds)
}

func createLoginState(baseURL string) (*loginState, error) {
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v2/plugin/auth/state?"+PlatformQS, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	for k, v := range anonHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("create login link: %w", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("create login link: invalid JSON: %w", err)
	}
	if env.Code != 0 || len(env.Data) == 0 {
		return nil, fmt.Errorf("failed to create login link: code=%d msg=%s", env.Code, env.Msg)
	}
	var st loginState
	if err := json.Unmarshal(env.Data, &st); err != nil {
		return nil, fmt.Errorf("parse login state: %w", err)
	}
	if st.State == "" {
		return nil, errors.New("login state is empty")
	}
	return &st, nil
}

// pollToken polls the token endpoint until the deadline; nil on timeout.
func pollToken(baseURL, state string) *Token {
	url := baseURL + "/v2/plugin/auth/token?state=" + urlEscape(state)
	deadline := time.Now().Add(loginWindow)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err == nil {
			for k, v := range anonHeaders() {
				req.Header.Set(k, v)
			}
			if resp, err := client.Do(req); err == nil {
				raw, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				var env envelope
				if json.Unmarshal(raw, &env) == nil && env.Code == 0 && len(env.Data) > 0 {
					var td Token
					if json.Unmarshal(env.Data, &td) == nil && td.AccessToken != "" {
						return &td
					}
				}
			}
		}
		time.Sleep(pollInterval)
	}
	return nil
}

// openBrowser launches url in the OS default browser without blocking.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("Could not open the browser automatically: %v\n", err)
		fmt.Printf("Open this URL manually: %s\n", url)
		return
	}
	// Reap the child so it does not linger as a zombie.
	go func() { _ = cmd.Wait() }()
}

func hostOf(baseURL string) string {
	s := baseURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	return s
}

func orString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// unixAfter returns the unix timestamp reached after the given duration.
func unixAfter(start time.Time, d any) any {
	var sec float64
	switch t := d.(type) {
	case float64:
		sec = t
	case string:
		if _, err := fmt.Sscanf(t, "%f", &sec); err != nil {
			return nil
		}
	default:
		return nil
	}
	return start.Add(time.Duration(sec * float64(time.Second))).Unix()
}

// urlEscape percent-encodes a string for use inside a query value.
func urlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
