package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"codex-workbuddyai-setup/internal/fsx"
	"codex-workbuddyai-setup/internal/paths"
)

// vendoredCatalog ships the pre-generated Codex catalogue inside the binary:
// setup needs no network round-trip to WorkBuddyAI and the exe is
// self-contained. `wbai models` remains the escape hatch to regenerate it
// from upstream (app cache + live API) when the model lineup changes.
//
//go:embed workbuddyai-models.json
var vendoredCatalog []byte

// installCatalog writes the embedded catalogue to ~/.codex, optionally
// filtered to the given model ids (in that order). It returns the installed
// path and the slugs that survived the filter.
func installCatalog(only []string) (string, []string, error) {
	var doc struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.Unmarshal(vendoredCatalog, &doc); err != nil {
		return "", nil, fmt.Errorf("parse embedded catalog: %w", err)
	}

	if len(only) > 0 {
		known := map[string]bool{}
		for _, m := range doc.Models {
			known[m.Slug] = true
		}
		var unknown []string
		for _, id := range only {
			if !known[id] {
				unknown = append(unknown, id)
			}
		}
		if len(unknown) > 0 {
			return "", nil, fmt.Errorf("unknown model id(s): %v", unknown)
		}
		filtered := doc.Models[:0]
		for _, want := range only {
			for _, m := range doc.Models {
				if m.Slug == want {
					filtered = append(filtered, m)
					break
				}
			}
		}
		doc.Models = filtered
	}

	slugs := make([]string, 0, len(doc.Models))
	for _, m := range doc.Models {
		slugs = append(slugs, m.Slug)
	}
	if len(slugs) == 0 {
		return "", nil, fmt.Errorf("embedded catalog has no models left after filtering")
	}

	dst, err := paths.CodexCatalog()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", nil, fmt.Errorf("create codex dir: %w", err)
	}
	pretty, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", nil, err
	}
	if err := fsx.AtomicWrite(dst, pretty, 0o644); err != nil {
		return "", nil, err
	}
	return dst, slugs, nil
}

// catalogDefaultModel returns the default slug from the installed catalogue:
// the first entry, which the vendored file orders with the recommended model
// first. When a filter is in force the first requested id wins.
func catalogDefaultModel(only []string) string {
	if len(only) > 0 {
		return only[0]
	}
	var doc struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	dst, err := paths.CodexCatalog()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(dst)
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	for _, m := range doc.Models {
		if m.Slug != "" {
			return m.Slug
		}
	}
	return ""
}
