// Package codexcfg patches ~/.codex/config.toml so the Codex CLI routes
// requests through the local gateway.
//
// The patcher is line-oriented rather than a full TOML implementation, but it
// does model the one TOML rule that matters here: bare `key = value` lines
// belong to whichever `[table]` header most recently preceded them. Root-level
// keys must therefore be emitted *before* the first table header, otherwise
// they silently become members of that table.
package codexcfg

import (
	"fmt"
	"os"
	"strings"
	"time"

	"codex-workbuddyai-setup/internal/fsx"
	"codex-workbuddyai-setup/internal/paths"
)

// ProviderName is the [model_providers.*] table this project owns.
const ProviderName = "workbuddyai"

// Marker introduces every region this project writes.
const Marker = "# --- codex-workbuddyai-setup (managed; do not edit by hand) ---"

// OwnedKeys are top-level keys this project strips on every `wbai config` run.
//
// Two of them are listed but no longer written: Codex 0.159 reports both
// `disable_response_storage` and `model_supports_reasoning_summaries` as
// unrecognized, so emitting them only produced warnings on every launch. They
// stay here so re-running `wbai config` cleans them out of existing files.
var OwnedKeys = map[string]bool{
	"model":                              true,
	"model_provider":                     true,
	"model_catalog_json":                 true,
	"model_reasoning_effort":             true,
	"disable_response_storage":           true,
	"model_supports_reasoning_summaries": true,
}

// Settings is the configuration applied to config.toml.
type Settings struct {
	Model         string
	CatalogFile   string
	BaseURL       string
	BearerToken   string
	ProviderLabel string
}

// ProviderTable returns the fully qualified table name owned by this project.
func ProviderTable() string {
	return "model_providers." + ProviderName
}

// Backup copies config.toml next to itself before any modification.
func Backup(src string) (string, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read config for backup: %w", err)
	}
	dst := fmt.Sprintf("%s.wbai-%s.bak", src, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return "", fmt.Errorf("write config backup: %w", err)
	}
	return dst, nil
}

// Apply loads config.toml, patches it, and writes it back.
// It returns the backup path (empty when the file did not exist).
func Apply(s Settings) (configPath, backupPath string, err error) {
	configPath, err = paths.CodexConfig()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dirOf(configPath), 0o755); err != nil {
		return "", "", fmt.Errorf("create codex dir: %w", err)
	}
	backupPath, err = Backup(configPath)
	if err != nil {
		return "", "", err
	}

	var original string
	if raw, err := os.ReadFile(configPath); err == nil {
		original = string(raw)
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("read config: %w", err)
	}

	updated := Patch(original, s)
	if dup := fsx.UniqueRootKeys(updated); dup != "" {
		return "", backupPath, fmt.Errorf("refusing to write %s: duplicate top-level key %q; original file untouched", configPath, dup)
	}
	if err := fsx.AtomicWrite(configPath, []byte(updated), 0o600); err != nil {
		return "", backupPath, fmt.Errorf("write config: %w", err)
	}
	return configPath, backupPath, nil
}

// Patch strips every region this project owns and rewrites it, keeping the
// user's own content untouched and in order.
func Patch(original string, s Settings) string {
	root, tables := splitDoc(original)

	keptRoot := trimBlank(stripRootKeys(strings.Split(root, "\n")))
	var keptTables [][]string
	for _, tbl := range tables {
		if headerOf(tbl[0]) == ProviderTable() {
			continue
		}
		if body := trimBlank(stripManaged(tbl)); len(body) > 0 {
			keptTables = append(keptTables, body)
		}
	}

	out := make([]string, 0, len(keptRoot)+16)
	out = append(out, keptRoot...)
	if len(out) > 0 {
		out = append(out, "")
	}
	out = append(out, rootBlock(s)...)
	for _, tbl := range keptTables {
		out = append(out, "")
		out = append(out, tbl...)
	}
	out = append(out, "")
	out = append(out, providerBlock(s)...)

	return strings.Join(out, "\n") + "\n"
}

// splitDoc separates the document root (lines before the first table header)
// from the ordered table blocks that follow.
func splitDoc(src string) (string, [][]string) {
	var root []string
	var tables [][]string
	var current []string

	for _, line := range strings.Split(src, "\n") {
		if h := headerOf(line); h != "" {
			if current != nil {
				tables = append(tables, current)
			}
			current = []string{line}
			continue
		}
		if current == nil {
			root = append(root, line)
		} else {
			current = append(current, line)
		}
	}
	if current != nil {
		tables = append(tables, current)
	}
	return strings.Join(root, "\n"), tables
}

// headerOf returns the table name when the line opens a [table] or [[array]].
func headerOf(line string) string {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") {
		return ""
	}
	if strings.HasPrefix(t, "[[") {
		if end := strings.Index(t, "]]"); end > 2 {
			return strings.TrimSpace(t[2:end])
		}
		return ""
	}
	if end := strings.Index(t, "]"); end > 1 {
		return strings.TrimSpace(t[1:end])
	}
	return ""
}

// stripManaged removes the marker line plus the owned keys that follow it.
// It works inside table bodies too, which is what makes re-patching idempotent
// even when an earlier version wrote the root keys into the wrong table.
func stripManaged(lines []string) []string {
	var out []string
	managed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == Marker {
			managed = true
			continue
		}
		if managed {
			if key, ok := rootKey(trimmed); ok && OwnedKeys[key] {
				continue
			}
			if trimmed == "" {
				continue
			}
			managed = false
		}
		out = append(out, line)
	}
	return out
}

// stripRootKeys removes every root-area line this project owns, whether or not
// it sits under this project's marker: other switchers (e.g. codex-longcat-setup)
// write the same top-level keys without our marker, and leaving them behind
// would produce duplicate keys that TOML rejects.
func stripRootKeys(lines []string) []string {
	stripped := stripManaged(lines)
	var out []string
	for _, line := range stripped {
		if key, ok := rootKey(strings.TrimSpace(line)); ok && OwnedKeys[key] {
			continue
		}
		out = append(out, line)
	}
	return out
}

// rootKey extracts the key name from a `key = value` line.
func rootKey(line string) (string, bool) {
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
		return "", false
	}
	eq := strings.Index(line, "=")
	if eq <= 0 {
		return "", false
	}
	key := strings.TrimSpace(line[:eq])
	if key == "" || strings.ContainsAny(key, "\"' ") {
		return "", false
	}
	return key, true
}

func trimBlank(lines []string) []string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start >= end {
		return nil
	}
	return lines[start:end]
}

// rootBlock renders the document-root keys. These must precede the first table
// header, otherwise TOML attaches them to that table.
func rootBlock(s Settings) []string {
	return []string{
		Marker,
		fmt.Sprintf("model_provider = %q", ProviderName),
		fmt.Sprintf("model = %q", s.Model),
		fmt.Sprintf("model_catalog_json = %q", catalogFile(s)),
		`model_reasoning_effort = "high"`,
	}
}

// providerBlock renders the [model_providers.workbuddyai] table.
func providerBlock(s Settings) []string {
	label := s.ProviderLabel
	if label == "" {
		label = "workbuddyai"
	}
	token := s.BearerToken
	if token == "" {
		token = "workbuddyai-local"
	}
	return []string{
		Marker,
		fmt.Sprintf("[%s]", ProviderTable()),
		fmt.Sprintf("name = %q", label),
		fmt.Sprintf("base_url = %q", s.BaseURL),
		`wire_api = "responses"`,
		"requires_openai_auth = false",
		fmt.Sprintf("experimental_bearer_token = %q", token),
	}
}

func catalogFile(s Settings) string {
	if s.CatalogFile != "" {
		return s.CatalogFile
	}
	return paths.CatalogFileName
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '\\' || p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
