package fsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteReplacesContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("old = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(p, []byte("new = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new = 2\n" {
		t.Errorf("content = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestUniqueRootKeys(t *testing.T) {
	cases := map[string]string{
		"model_provider = \"a\"\nmodel_provider = \"b\"\n": "model_provider",
		"model = \"a\"\n\n[windows]\nsandbox = \"e\"\n":    "",
		"# comment\nmodel = \"a\"\nmodel = \"b\"\n":        "model",
		"model = \"a\"\n[desktop]\nmodel = \"b\"\n":        "", // section keys are not root keys
		"": "",
	}
	for config, want := range cases {
		if got := UniqueRootKeys(config); got != want {
			t.Errorf("UniqueRootKeys(%q) = %q, want %q", config, got, want)
		}
	}
}
