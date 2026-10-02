package upstream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Model is one entry of the `models[]` array in the model payload.
type Model struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	DescriptionEn    string   `json:"descriptionEn"`
	DescriptionZh    string   `json:"descriptionZh"`
	MaxInputTokens   int      `json:"maxInputTokens"`
	MaxOutputTokens  int      `json:"maxOutputTokens"`
	SupportsImages   bool     `json:"supportsImages"`
	SupportsToolCall bool     `json:"supportsToolCall"`
	Temperature      *float64 `json:"temperature"`
	IsDefault        bool     `json:"isDefault"`
	Credits          any      `json:"credits"`
}

// Agent is one entry of the `agents[]` array; only its model list matters here.
type Agent struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Models      []string `json:"models"`
}

// AuthAttributes carries the header contract advertised by the payload.
type AuthAttributes struct {
	PrefixPath  string `json:"prefixPath"`
	TokenHeader string `json:"tokenHeader"`
	TokenType   string `json:"tokenType"`
	UsernameHdr string `json:"usernameHeader"`
}

// Authentication is the `authentication` object in the model payload.
type Authentication struct {
	ID         string         `json:"id"`
	Label      string         `json:"label"`
	Type       string         `json:"type"`
	Attributes AuthAttributes `json:"attributes"`
}

// DefaultRelatedModels points at the lite and reasoning defaults.
type DefaultRelatedModels struct {
	Lite      string `json:"lite"`
	Reasoning string `json:"reasoning"`
}

// ModelSet is the decoded subset of the model payload this project depends on.
type ModelSet struct {
	Endpoint        string               `json:"endpoint"`
	StagingEndpoint string               `json:"stagingEndpoint"`
	DeploymentType  string               `json:"deploymentType"`
	Platform        string               `json:"platform"`
	Authentication  Authentication       `json:"authentication"`
	DefaultRelated  DefaultRelatedModels `json:"defaultRelatedModels"`
	Models          []Model              `json:"models"`
	Agents          []Agent              `json:"agents"`

	// Data is the untouched `data` object, persisted verbatim to models.json.
	Data json.RawMessage `json:"-"`
}

// envelope is the shared API response wrapper.
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// FetchModels retrieves and decodes the model payload for the given token.
func FetchModels(cfg Config, token, uid string) (*ModelSet, error) {
	req, err := http.NewRequest(http.MethodGet, cfg.ModelsURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CodeBuddyCode/1.0")
	if uid != "" {
		req.Header.Set("X-User-Id", uid)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models request failed: %w", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read models response: %w", readErr)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models request failed: HTTP %d", resp.StatusCode)
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("models response is not valid JSON: %w", err)
	}
	if len(env.Data) == 0 {
		return nil, fmt.Errorf(`models response has no "data" field (code=%d msg=%s)`, env.Code, env.Msg)
	}

	set := &ModelSet{}
	if err := json.Unmarshal(env.Data, set); err != nil {
		return nil, fmt.Errorf("decode models payload: %w", err)
	}
	set.Data = env.Data
	if len(set.Models) == 0 {
		return nil, fmt.Errorf("models payload contains no models")
	}
	return set, nil
}

// AllModelIDs unions `models[].id` with every id referenced by `agents[]`,
// preserving first-seen order. The original implementation only fell back to
// agents when `models` was empty, which silently dropped agent-only models.
func (m *ModelSet) AllModelIDs() []string {
	seen := make(map[string]bool)
	var ids []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, model := range m.Models {
		add(model.ID)
	}
	for _, agent := range m.Agents {
		for _, id := range agent.Models {
			add(id)
		}
	}
	return ids
}

// ResolveConfig derives connection settings from the model payload.
func (m *ModelSet) ResolveConfig(fallback Config) Config {
	cfg := fallback
	if m.Endpoint != "" {
		cfg.BaseURL = m.Endpoint
	}
	cfg.ChatPath = DefaultChatPath
	if h := m.Authentication.Attributes.TokenHeader; h != "" {
		cfg.TokenHeader = h
	}
	if t := m.Authentication.Attributes.TokenType; t != "" {
		cfg.TokenType = t
	}
	if h := m.Authentication.Attributes.UsernameHdr; h != "" {
		cfg.UsernameHeader = h
	}
	return cfg
}
