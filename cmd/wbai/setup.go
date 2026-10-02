package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"codex-workbuddyai-setup/internal/catalog"
	"codex-workbuddyai-setup/internal/codexcfg"
	"codex-workbuddyai-setup/internal/cred"
	"codex-workbuddyai-setup/internal/paths"
	"codex-workbuddyai-setup/internal/prefs"
	"codex-workbuddyai-setup/internal/upstream"
)

// ensureCredentials returns stored credentials, running an interactive login
// when none exist yet.
func ensureCredentials() (*cred.Credentials, error) {
	c, err := cred.Load()
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, cred.ErrNotLoggedIn) {
		return nil, err
	}
	fmt.Println("No credentials found, starting login...")
	return cred.Login(cred.DefaultBaseURL)
}

// resolveModelSet picks where the catalogue comes from.
//
// auto (default) prefers the desktop app's cached catalogue, because the
// CLI-facing model endpoint returns a narrowed list, and overlays the live API
// response on top for fresher metadata.
func resolveModelSet(source string) (*upstream.ModelSet, []string, error) {
	switch source {
	case "api":
		set, err := fetchAPISet()
		return set, []string{"api"}, err
	case "cache":
		set, path, err := upstream.LoadAppCache()
		if err != nil {
			return nil, nil, err
		}
		if set == nil {
			return nil, nil, fmt.Errorf("app cache not found (looked in %s)",
				strings.Join(upstream.AppCachePaths(), ", "))
		}
		return set, []string{"app cache: " + path}, nil
	case "", "auto":
		cacheSet, cachePath, cacheErr := upstream.LoadAppCache()
		if cacheErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", cacheErr)
		}
		apiSet, apiErr := fetchAPISet()
		switch {
		case cacheSet != nil && apiSet != nil:
			return upstream.Merge(cacheSet, apiSet),
				[]string{"app cache: " + cachePath, "api overlay"}, nil
		case cacheSet != nil:
			if apiErr != nil {
				fmt.Fprintf(os.Stderr, "note: live model API unavailable (%v); using the app cache only\n", apiErr)
			}
			return cacheSet, []string{"app cache: " + cachePath}, nil
		case apiSet != nil:
			return apiSet, []string{"api"}, nil
		default:
			return nil, nil, apiErr
		}
	default:
		return nil, nil, fmt.Errorf("unknown --source %q (want auto, cache or api)", source)
	}
}

// fetchAPISet retrieves the model payload, transparently repairing an expired
// token: first by refreshing, then by starting a fresh interactive login.
func fetchAPISet() (*upstream.ModelSet, error) {
	cfg, _ := upstream.LoadConfig()

	c, err := ensureCredentials()
	if err != nil {
		return nil, err
	}
	set, err := upstream.FetchModels(cfg, c.AccessToken, c.UID)
	if errors.Is(err, upstream.ErrUnauthorized) {
		fmt.Println("Access token rejected, refreshing...")
		if refreshed, rerr := cred.Refresh(c); rerr == nil {
			c = refreshed
			set, err = upstream.FetchModels(cfg, c.AccessToken, c.UID)
		}
	}
	if errors.Is(err, upstream.ErrUnauthorized) {
		fmt.Println("Token unusable, starting a new login...")
		if c, err = cred.Login(cred.DefaultBaseURL); err == nil {
			set, err = upstream.FetchModels(cfg, c.AccessToken, c.UID)
		}
	}
	return set, err
}

// splitOnly parses a comma-separated --only value.
func splitOnly(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// persistModels regenerates the Codex catalogue from a fetched payload and
// writes it straight to ~/.codex. This is the `wbai models` refresh path; the
// regular setup flow installs the embedded catalogue instead and never needs
// the network for models.
func persistModels(set *upstream.ModelSet, only []string) (*catalog.Catalog, error) {
	cfg, _ := upstream.LoadConfig()
	resolved := set.ResolveConfig(cfg)
	if err := upstream.SaveConfig(resolved); err != nil {
		return nil, err
	}

	cat, err := catalog.Build(set, only)
	if err != nil {
		return nil, err
	}
	codexPath, err := catalog.Write(cat)
	if err != nil {
		return nil, err
	}
	if err := prefs.Save(&prefs.Prefs{Only: only}); err != nil {
		return nil, err
	}
	fmt.Printf("Upstream config: %s\n", mustPath(paths.Upstream))
	fmt.Printf("Model catalogue: %s\n", codexPath)
	return cat, nil
}

// defaultModel resolves the model Codex should use: the persisted filter's
// first entry, else the first slug of the installed catalogue.
func defaultModel() string {
	return catalogDefaultModel(prefs.Resolve(nil))
}

func mustPath(fn func() (string, error)) string {
	p, err := fn()
	if err != nil {
		return ""
	}
	return p
}

func cmdLogin(args []string) error {
	fs := newFlagSet("login")
	force := fs.Bool("force", false, "log in again even if credentials already exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*force {
		if _, err := cred.Load(); err == nil {
			fmt.Printf("Already logged in (%s)\n", mustPath(paths.Credentials))
			fmt.Println("Use --force to sign in again.")
			return nil
		}
	}
	c, err := cred.Login(cred.DefaultBaseURL)
	if err != nil {
		return err
	}
	fmt.Printf("\nLogged in as UID %s (%s)\n", c.UID, c.Domain)
	fmt.Printf("Credentials: %s\n", mustPath(paths.Credentials))
	return nil
}

// cmdModels is the optional refresh path: setup ships an embedded catalogue
// and does not call this; run it only when the upstream model lineup changed
// and a rebuilt binary is not an option.
func cmdModels(args []string) error {
	fs := newFlagSet("models")
	only := fs.String("only", "", "comma-separated model ids to expose (default: remembered selection)")
	all := fs.Bool("all", false, "expose every model the upstream reports")
	source := fs.String("source", "auto", "catalogue source: auto, cache (desktop app) or api")
	if err := fs.Parse(args); err != nil {
		return err
	}

	filter := prefs.Resolve(splitOnly(*only))
	if *all {
		filter = nil
	}

	set, sources, err := resolveModelSet(*source)
	if err != nil {
		return err
	}
	for _, s := range sources {
		fmt.Printf("Source         : %s\n", s)
	}
	cat, err := persistModels(set, filter)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("Models in catalogue: %d\n", len(cat.Models))
	for i, m := range cat.Models {
		if i >= 50 {
			fmt.Printf("  ... %d more (see the catalogue file)\n", len(cat.Models)-50)
			break
		}
		fmt.Printf("  - %-20s ctx=%d %v\n", m.Slug, m.ContextWindow, m.InputModalities)
	}
	fmt.Printf("\nSelected default: %s\n", catalog.DefaultModel(set, filter))
	return nil
}

func cmdConfig(args []string) error {
	fs := newFlagSet("config")
	addr := fs.String("addr", defaultAddr, "gateway address Codex should call")
	model := fs.String("model", "", "model id to select (defaults to the recommended one)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	chosen := *model
	if chosen == "" {
		chosen = defaultModel()
	}
	if chosen == "" {
		return errors.New("no model available; run `wbai models` first")
	}

	baseURL := "http://" + *addr + "/v1"
	configPath, backupPath, err := codexcfg.Apply(codexcfg.Settings{
		Model:       chosen,
		CatalogFile: paths.CatalogFileName,
		BaseURL:     baseURL,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Config patched : %s\n", configPath)
	if backupPath != "" {
		fmt.Printf("Backup         : %s\n", backupPath)
	}
	fmt.Printf("Provider       : %s -> %s (wire_api=responses)\n", codexcfg.ProviderName, baseURL)
	fmt.Printf("Model          : %s\n", chosen)
	return nil
}

func cmdSetup(args []string) error {
	fs := newFlagSet("setup")
	addr := fs.String("addr", defaultAddr, "gateway address Codex should call")
	model := fs.String("model", "", "model id to select (defaults to the recommended one)")
	only := fs.String("only", "", "comma-separated model ids to expose (default: remembered selection)")
	all := fs.Bool("all", false, "expose every model the upstream reports")
	source := fs.String("source", "auto", "catalogue source: auto, cache (desktop app) or api")
	if err := fs.Parse(args); err != nil {
		return err
	}

	filter := prefs.Resolve(splitOnly(*only))
	if *all {
		filter = nil
	}

	fmt.Println("== 1/3 Login ==")
	cfg, _ := upstream.LoadConfig()
	if _, err := ensureCredentials(); err != nil {
		return err
	}
	_ = cfg

	fmt.Println("\n== 2/3 Models ==")
	set, sources, err := resolveModelSet(*source)
	if err != nil {
		return err
	}
	for _, s := range sources {
		fmt.Printf("Source         : %s\n", s)
	}
	if _, err := persistModels(set, filter); err != nil {
		return err
	}

	fmt.Println("\n== 3/3 Codex config ==")
	chosen := *model
	if chosen == "" {
		chosen = catalog.DefaultModel(set, filter)
	}
	baseURL := "http://" + *addr + "/v1"
	configPath, backupPath, err := codexcfg.Apply(codexcfg.Settings{
		Model:       chosen,
		CatalogFile: paths.CatalogFileName,
		BaseURL:     baseURL,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Config patched : %s\n", configPath)
	if backupPath != "" {
		fmt.Printf("Backup         : %s\n", backupPath)
	}

	fmt.Println()
	fmt.Println("Setup complete. Start the gateway with:")
	fmt.Printf("    wbai serve --addr %s\n", *addr)
	return nil
}
