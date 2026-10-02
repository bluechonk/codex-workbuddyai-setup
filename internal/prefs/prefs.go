// Package prefs persists the small set of user choices that must survive across
// `wbai models`, `wbai config` and `wbai setup` runs.
package prefs

import (
	"encoding/json"
	"fmt"
	"os"

	"codex-workbuddyai-setup/internal/paths"
)

// Prefs is the on-disk preference file (~/.codex-workbuddyai-setup/config.json).
type Prefs struct {
	// Only restricts the generated catalogue to these model ids, in order.
	// Empty means "every model the upstream reports".
	Only []string `json:"only,omitempty"`
}

// Load reads the preference file; a missing file yields empty preferences.
func Load() *Prefs {
	path, err := paths.Prefs()
	if err != nil {
		return &Prefs{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return &Prefs{}
	}
	var p Prefs
	if err := json.Unmarshal(raw, &p); err != nil {
		return &Prefs{}
	}
	return &p
}

// Save writes the preference file with 0600 permissions.
func Save(p *Prefs) error {
	path, err := paths.Prefs()
	if err != nil {
		return err
	}
	if _, err := paths.EnsureDir(); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}
	pretty, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encode preferences: %w", err)
	}
	return os.WriteFile(path, pretty, 0o600)
}

// DefaultOnly is the set of models this installation exposes to Codex.
// Kept as a slice so it can be widened without changing the file format.
var DefaultOnly = []string{"hy4-preview-f", "deepseek-v4.1-flash"}

// Resolve returns the effective model filter: the explicit argument when given,
// otherwise whatever is persisted, otherwise DefaultOnly.
func Resolve(explicit []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	if p := Load(); len(p.Only) > 0 {
		return p.Only
	}
	return DefaultOnly
}
