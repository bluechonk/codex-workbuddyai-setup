package codexcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSettings() Settings {
	return Settings{
		Model:       "hy4-preview",
		CatalogFile: "workbuddyai-models.json",
		BaseURL:     "http://127.0.0.1:8787/v1",
	}
}

// firstTableIndex returns the index of the first line opening a table.
func firstTableIndex(lines []string) int {
	for i, line := range lines {
		if headerOf(line) != "" {
			return i
		}
	}
	return -1
}

// TestRootKeysPrecedeTables is the regression test for the original bug: root
// keys appended after a table header become members of that table.
func TestRootKeysPrecedeTables(t *testing.T) {
	src := `web_search = "disabled"

[model_providers.longcat]
name = "longcat"
base_url = "https://api.longcat.chat/openai/v1"
`
	out := Patch(src, testSettings())
	lines := strings.Split(out, "\n")

	tbl := firstTableIndex(lines)
	if tbl < 0 {
		t.Fatal("expected at least one table header")
	}
	for _, key := range []string{"model_provider", "model", "model_catalog_json"} {
		idx := -1
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), key+" ") {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Fatalf("%s not written", key)
		}
		if idx > tbl {
			t.Errorf("%s written at line %d, after the first table header at %d; TOML would attach it to that table", key, idx, tbl)
		}
	}
	if !strings.Contains(out, `web_search = "disabled"`) {
		t.Error("pre-existing root key was dropped")
	}
	if !strings.Contains(out, `[model_providers.longcat]`) {
		t.Error("pre-existing provider table was dropped")
	}
}

// TestPatchIsIdempotent guards against the duplicated managed block.
func TestPatchIsIdempotent(t *testing.T) {
	once := Patch(`web_search = "disabled"`+"\n", testSettings())
	twice := Patch(once, testSettings())
	if once != twice {
		t.Errorf("patch is not idempotent\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
	if n := strings.Count(twice, Marker); n != 2 {
		t.Errorf("expected exactly 2 managed markers, found %d", n)
	}
	if n := strings.Count(twice, "model = "); n != 1 {
		t.Errorf("expected exactly 1 model key, found %d", n)
	}
}

// TestRepairsMisplacedBlock covers configs already corrupted by the old
// patcher, where the managed root keys landed inside another provider table.
func TestRepairsMisplacedBlock(t *testing.T) {
	corrupted := `web_search = "disabled"

[model_providers.longcat]
name = "longcat"
` + Marker + `
model_provider = "workbuddyai"
model = "gpt-5.3-codex"
model_catalog_json = "workbuddyai-models.json"
disable_response_storage = true
model_reasoning_effort = "high"
model_supports_reasoning_summaries = true

[model_providers.workbuddyai]
name = "workbuddyai"
base_url = "http://127.0.0.1:8787/v1"
`
	out := Patch(corrupted, testSettings())
	if strings.Contains(out, "gpt-5.3-codex") {
		t.Error("stale model from the corrupted block survived")
	}
	if n := strings.Count(out, "model = "); n != 1 {
		t.Errorf("expected exactly 1 model key, found %d", n)
	}
	if n := strings.Count(out, "model_provider = "); n != 1 {
		t.Errorf("expected exactly 1 model_provider key, found %d", n)
	}
	if n := strings.Count(out, "[model_providers.workbuddyai]"); n != 1 {
		t.Errorf("expected exactly 1 provider table, found %d", n)
	}
	// The longcat table must still own its own keys and nothing else.
	lines := strings.Split(out, "\n")
	tbl := firstTableIndex(lines)
	next := len(lines)
	for i := tbl + 1; i < len(lines); i++ {
		if headerOf(lines[i]) != "" {
			next = i
			break
		}
	}
	body := lines[tbl+1 : next]
	for _, line := range body {
		key, ok := rootKey(strings.TrimSpace(line))
		if ok && OwnedKeys[key] {
			t.Errorf("managed key %q leaked into the longcat table body: %q", key, line)
		}
	}
	// The longcat table must keep its own key and nothing else.
	if !containsLine(body, `name = "longcat"`) {
		t.Errorf("longcat lost its own key:\n%s", strings.Join(body, "\n"))
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// TestEmptyConfig checks a config.toml that does not exist yet.
func TestEmptyConfig(t *testing.T) {
	out := Patch("", testSettings())
	if !strings.Contains(out, `model = "hy4-preview"`) {
		t.Error("model not written")
	}
	if !strings.Contains(out, "[model_providers.workbuddyai]") {
		t.Error("provider table not written")
	}
	if strings.HasPrefix(out, "\n") {
		t.Error("output should not start with a blank line")
	}
}

// TestArrayTablePreserved makes sure [[...]] headers are not mistaken for
// something we own.
func TestArrayTablePreserved(t *testing.T) {
	src := "[[hooks]]\nname = \"x\"\n"
	out := Patch(src, testSettings())
	if !strings.Contains(out, "[[hooks]]") {
		t.Error("array table was dropped")
	}
}

// Regression: switching back from another provider (e.g. codex-longcat-setup)
// whose top-level keys are not under our marker must not leave duplicate keys.
func TestPatchAfterForeignSwitcher(t *testing.T) {
	longcatConfig := `web_search = "disabled"

model_provider = "longcat"
model = "LongCat-2.5-Preview"
model_catalog_json = "C:/Users/u/.codex/models.json"
model_reasoning_effort = "high"
show_raw_agent_reasoning = true

[desktop]
enabled-reasoning-efforts = ["low", "medium", "high"]

[model_providers.longcat]
name = "longcat"
base_url = "https://api.longcat.chat/openai/v1"
wire_api = "responses"
env_key = "LONGCAT_API_KEY"
`
	out := Patch(longcatConfig, Settings{Model: "deepseek-v4.1-flash", CatalogFile: "workbuddyai-models.json", BaseURL: "http://127.0.0.1:8787/v1"})

	count := func(s string) int { return strings.Count(out, s) }
	for key, want := range map[string]int{
		`model_provider = "workbuddyai"`:                 1,
		`model_provider = "longcat"`:                     0,
		`model = "LongCat-2.5-Preview"`:                  0,
		`model = "deepseek-v4.1-flash"`:                  1,
		`model_catalog_json = "workbuddyai-models.json"`: 1,
		"[model_providers.longcat]":                      1, // foreign provider table is harmless, kept
		"[model_providers.workbuddyai]":                  1,
		`web_search = "disabled"`:                        1, // user content untouched
	} {
		if got := count(key); got != want {
			t.Errorf("%q count = %d, want %d\n---\n%s", key, got, want, out)
		}
	}
}

// Apply must refuse to publish a config with duplicate top-level keys instead
// of clobbering the file.
func TestApplyRejectsDuplicateRootKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir) // paths.Home resolves via os.UserHomeDir
	t.Setenv("HOME", dir)
	configPath := filepath.Join(dir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// A user-owned key pair that Patch cannot remove (not in OwnedKeys).
	original := "custom_thing = 1\ncustom_thing = 2\n"
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(Settings{Model: "m", CatalogFile: "c.json", BaseURL: "http://x"}); err == nil {
		t.Fatal("Apply must fail on duplicate root keys")
	}
	got, _ := os.ReadFile(configPath)
	if string(got) != original {
		t.Errorf("original file must stay untouched, got:\n%s", got)
	}
}
