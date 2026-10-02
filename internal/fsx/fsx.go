// Package fsx holds small filesystem helpers shared by the writer packages.
package fsx

import (
	"fmt"
	"os"
	"strings"
)

// AtomicWrite writes data via a temp file in the destination directory plus a
// rename, so a crash mid-write can never leave a truncated config or catalog
// behind. The temp file is removed if the rename fails.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}

// UniqueRootKeys reports the first duplicated top-level TOML key in config, or
// "" when there is none. Duplicate root keys make Codex refuse the whole
// config, so writers validate before publishing. The scan is line-based and
// stops at the first section header; multi-line root values are not expected
// in config.toml (base_instructions and friends were removed in Codex 0.159).
func UniqueRootKeys(config string) string {
	seen := map[string]bool{}
	for _, line := range strings.Split(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		eq := strings.Index(trimmed, "=")
		if eq <= 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(trimmed[:eq]), `"'`)
		if key == "" {
			continue
		}
		if seen[key] {
			return key
		}
		seen[key] = true
	}
	return ""
}
