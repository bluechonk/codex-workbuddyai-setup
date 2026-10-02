// Package paths resolves every file location used by codex-workbuddyai-setup.
//
// Layout (all under the user home directory):
//
//	~/.codex-workbuddyai-setup/
//	    credentials.json   access/refresh token + uid + domain
//	    upstream.json      resolved upstream endpoint + auth header names
//
//	~/.codex/
//	    config.toml                 Codex CLI config (patched, backup kept)
//	    workbuddyai-models.json     copy of model.json referenced by config.toml
package paths

import (
	"os"
	"path/filepath"
)

// DirName is the storage directory created inside the user home directory.
const DirName = ".codex-workbuddyai-setup"

// CodexDirName is the Codex CLI configuration directory.
const CodexDirName = ".codex"

// CatalogFileName is the bare file name referenced by `model_catalog_json`.
// Codex resolves a bare name relative to ~/.codex, so a copy is placed there.
// plural form matches Codex's own `models.json` naming convention.
const CatalogFileName = "workbuddyai-models.json"

// Home returns the user home directory.
func Home() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", os.ErrNotExist
	}
	return dir, nil
}

// Dir returns ~/.codex-workbuddyai-setup without creating it.
func Dir() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DirName), nil
}

// EnsureDir returns ~/.codex-workbuddyai-setup, creating it when missing.
func EnsureDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Credentials is the credential file inside the storage directory.
func Credentials() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// Prefs is the persisted user preference file inside the storage directory.
func Prefs() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Upstream is the resolved upstream descriptor inside the storage directory.
func Upstream() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "upstream.json"), nil
}

// CodexDir returns ~/.codex.
func CodexDir() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, CodexDirName), nil
}

// CodexConfig returns ~/.codex/config.toml.
func CodexConfig() (string, error) {
	dir, err := CodexDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// CodexCatalog returns the catalog copy that config.toml references.
func CodexCatalog() (string, error) {
	dir, err := CodexDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, CatalogFileName), nil
}
