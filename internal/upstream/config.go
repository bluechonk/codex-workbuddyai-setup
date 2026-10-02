// Package upstream talks to the WorkBuddyAI API: it fetches the model
// catalogue, resolves the connection settings, and streams chat completions.
package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"codex-workbuddyai-setup/internal/paths"
)

// Config describes how to reach the WorkBuddyAI chat endpoint.
type Config struct {
	BaseURL        string `json:"baseUrl"`
	ChatPath       string `json:"chatPath"`
	TokenHeader    string `json:"tokenHeader"`
	TokenType      string `json:"tokenType"`
	UsernameHeader string `json:"usernameHeader"`
}

// DefaultChatPath is the verified chat-completions route.
//
// The model payload also advertises `authentication.attributes.prefixPath`
// ("/plugin"), but POST /plugin/v2/chat/completions answers 404 while
// POST /v2/chat/completions answers 200, so the prefix is deliberately unused.
const DefaultChatPath = "/v2/chat/completions"

// ErrNoUpstream means upstream.json has not been produced by `wbai models` yet.
var ErrNoUpstream = errors.New("upstream config missing; run `wbai models`")

// ErrUnauthorized means the access token was rejected by the upstream API.
var ErrUnauthorized = errors.New("upstream rejected the access token (HTTP 401/403)")

// defaultConfig is used before any model payload has been fetched.
func defaultConfig() Config {
	return Config{
		BaseURL:        "https://www.workbuddy.ai",
		ChatPath:       DefaultChatPath,
		TokenHeader:    "Authorization",
		TokenType:      "bearerToken",
		UsernameHeader: "X-User-Id",
	}
}

// LoadConfig reads the resolved upstream descriptor written by `wbai models`.
func LoadConfig() (Config, error) {
	path, err := paths.Upstream()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), ErrNoUpstream
		}
		return Config{}, fmt.Errorf("read upstream config: %w", err)
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse upstream config: %w", err)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultConfig().BaseURL
	}
	if cfg.ChatPath == "" {
		cfg.ChatPath = DefaultChatPath
	}
	return cfg, nil
}

// SaveConfig persists the resolved upstream descriptor.
func SaveConfig(cfg Config) error {
	path, err := paths.Upstream()
	if err != nil {
		return err
	}
	if _, err := paths.EnsureDir(); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}
	pretty, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode upstream config: %w", err)
	}
	return os.WriteFile(path, pretty, 0o600)
}

// ChatURL returns the fully qualified chat-completions URL.
func (c Config) ChatURL() string {
	return trimSlash(c.BaseURL) + ensureSlash(c.ChatPath)
}

// ModelsURL returns the endpoint listing available models.
func (c Config) ModelsURL() string {
	return trimSlash(c.BaseURL) + "/v2/enterprises/personal/models"
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func ensureSlash(s string) string {
	if s == "" {
		return "/"
	}
	if s[0] != '/' {
		return "/" + s
	}
	return s
}
