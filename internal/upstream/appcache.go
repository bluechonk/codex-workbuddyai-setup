package upstream

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppCacheFileName is the desktop app's cached account product config.
const AppCacheFileName = "acc-product-config-v3.json"

// appCacheDirs are the directories searched for that cache, in order. The
// international build uses ~/.workbuddy-ai, the CN build ~/.workbuddy.
var appCacheDirs = []string{".workbuddy-ai", ".workbuddy"}

// AppCachePaths lists every candidate cache location, most preferred first.
func AppCachePaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(appCacheDirs))
	for _, dir := range appCacheDirs {
		out = append(out, filepath.Join(home, dir, "cache", AppCacheFileName))
	}
	return out
}

// LoadAppCache reads the catalogue the desktop app caches on every launch.
//
// This matters because the two sources disagree: the CLI-facing
// /v2/enterprises/personal/models endpoint returns a narrowed list (18 models
// here), while the app cache carries the full picker (29), including
// deepseek-v4.1-flash, gpt-6-astra, gpt-6-sol and others. The cache is
// therefore preferred, with the endpoint overlaid on top for fresh metadata.
//
// It returns the decoded set and the path it came from; a missing cache is not
// an error, it just yields a nil set.
func LoadAppCache() (*ModelSet, string, error) {
	for _, path := range AppCachePaths() {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, path, fmt.Errorf("read app cache: %w", err)
		}
		set, err := decodeProductConfig(raw)
		if err != nil {
			return nil, path, fmt.Errorf("parse app cache %s: %w", path, err)
		}
		if len(set.Models) == 0 {
			continue
		}
		return set, path, nil
	}
	return nil, "", nil
}

// decodeProductConfig pulls the model list out of the product config document.
// The array lives at the top level today, but it is located by search so a
// future nesting change does not break us.
func decodeProductConfig(raw []byte) (*ModelSet, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	models, ok := findModelArray(doc)
	if !ok {
		return nil, nil
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		return nil, err
	}

	set := &ModelSet{Data: raw}
	if err := json.Unmarshal(encoded, &set.Models); err != nil {
		return nil, err
	}
	// Reuse the same field decoding as the API payload for the shared bits.
	var meta struct {
		Endpoint        string         `json:"endpoint"`
		StagingEndpoint string         `json:"stagingEndpoint"`
		DeploymentType  string         `json:"deploymentType"`
		Authentication  Authentication `json:"authentication"`
	}
	if err := json.Unmarshal(raw, &meta); err == nil {
		set.Endpoint = meta.Endpoint
		set.StagingEndpoint = meta.StagingEndpoint
		set.DeploymentType = meta.DeploymentType
		set.Authentication = meta.Authentication
	}
	return set, nil
}

// findModelArray locates the first `models` array of objects carrying an id.
func findModelArray(node any) ([]any, bool) {
	switch v := node.(type) {
	case map[string]any:
		if arr, ok := v["models"].([]any); ok && len(arr) > 0 {
			if first, ok := arr[0].(map[string]any); ok {
				if id, _ := first["id"].(string); id != "" {
					return arr, true
				}
			}
		}
		for _, child := range v {
			if arr, ok := findModelArray(child); ok {
				return arr, true
			}
		}
	case []any:
		for _, child := range v {
			if arr, ok := findModelArray(child); ok {
				return arr, true
			}
		}
	}
	return nil, false
}

// nonChatExact are ids that exist for UI routing rather than direct use.
var nonChatExact = map[string]bool{
	"lite": true,
}

// nonChatPrefixes / nonChatSuffixes cover ids Codex cannot drive.
var (
	nonChatPrefixes = []string{"codewise-", "completion-", "gpt-image-", "seedance-"}
	nonChatSuffixes = []string{"-image-alpha", "-image-alpha-edit", "-taco-completion"}
)

// IsChatModel reports whether Codex can actually use the given model id.
// Image, video, completion-only and UI-alias entries are filtered out.
func IsChatModel(id string) bool {
	if id == "" || nonChatExact[id] {
		return false
	}
	for _, p := range nonChatPrefixes {
		if strings.HasPrefix(id, p) {
			return false
		}
	}
	for _, s := range nonChatSuffixes {
		if strings.HasSuffix(id, s) {
			return false
		}
	}
	return true
}

// Merge overlays live metadata on top of a broader base catalogue.
//
// Base order is preserved and base-only models survive, so a wider app cache is
// never narrowed by a smaller API response; matching ids simply take the
// overlay's fresher metadata.
func Merge(base, overlay *ModelSet) *ModelSet {
	if base == nil {
		return overlay
	}
	if overlay == nil {
		return base
	}
	out := &ModelSet{
		Endpoint:        pick(base.Endpoint, overlay.Endpoint),
		StagingEndpoint: pick(base.StagingEndpoint, overlay.StagingEndpoint),
		DeploymentType:  pick(base.DeploymentType, overlay.DeploymentType),
		Platform:        pick(base.Platform, overlay.Platform),
		Authentication:  base.Authentication,
		DefaultRelated:  base.DefaultRelated,
		Data:            overlay.Data,
	}
	if out.Authentication.ID == "" {
		out.Authentication = overlay.Authentication
	}

	index := make(map[string]int, len(base.Models))
	out.Models = make([]Model, 0, len(base.Models)+len(overlay.Models))
	for _, m := range base.Models {
		if _, dup := index[m.ID]; dup {
			continue
		}
		index[m.ID] = len(out.Models)
		out.Models = append(out.Models, m)
	}
	for _, m := range overlay.Models {
		if at, ok := index[m.ID]; ok {
			out.Models[at] = m
			continue
		}
		index[m.ID] = len(out.Models)
		out.Models = append(out.Models, m)
	}
	if len(overlay.Agents) > 0 {
		out.Agents = overlay.Agents
	} else {
		out.Agents = base.Agents
	}
	return out
}

func pick(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

// ChatModelIDs returns the usable ids in catalogue order.
func (m *ModelSet) ChatModelIDs() []string {
	var out []string
	for _, id := range m.AllModelIDs() {
		if IsChatModel(id) {
			out = append(out, id)
		}
	}
	return out
}
