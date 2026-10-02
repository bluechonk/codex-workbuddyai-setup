package main

import (
	"fmt"
	"os"

	"codex-workbuddyai-setup/internal/paths"
)

func storageDir() (string, error) { return paths.Dir() }

// must unwraps a (string, error) pair, collapsing errors to an empty string.
func must(p string, err error) string {
	if err != nil {
		return ""
	}
	return p
}

func storageFile(name string) (string, error) {
	dir, err := paths.Dir()
	if err != nil {
		return "", err
	}
	return dir + string(os.PathSeparator) + name, nil
}

func codexConfigPath() (string, error) { return paths.CodexConfig() }

func codexCatalogPath() (string, error) { return paths.CodexCatalog() }

// requireFile reports whether a file exists and is non-empty.
func requireFile(path string) error {
	if path == "" {
		return fmt.Errorf("path unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("missing %s", path)
		}
		return err
	}
	if info.Size() == 0 {
		return fmt.Errorf("empty %s", path)
	}
	return nil
}
