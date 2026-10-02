// Package catalog converts a WorkBuddyAI model payload into the model
// catalogue format that the Codex CLI reads via `model_catalog_json`.
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"codex-workbuddyai-setup/internal/fsx"
	"codex-workbuddyai-setup/internal/paths"
	"codex-workbuddyai-setup/internal/upstream"
)

// BaseInstructions mirrors the Codex system prompt so the catalogue entry is
// self-consistent with what Codex sends as `instructions`.
const BaseInstructions = "You are Codex, a coding agent. You and the user share the same workspace and collaborate to achieve the user's goals."

// ReasoningLevel is one entry of `supported_reasoning_levels`.
type ReasoningLevel struct {
	Description string `json:"description"`
	Effort      string `json:"effort"`
}

// TruncationPolicy matches the Codex truncation defaults.
type TruncationPolicy struct {
	Limit int    `json:"limit"`
	Mode  string `json:"mode"`
}

// Entry is a single model in the generated catalogue.
type Entry struct {
	AdditionalSpeedTiers          []any            `json:"additional_speed_tiers"`
	AvailabilityNux               any              `json:"availability_nux"`
	BaseInstructions              string           `json:"base_instructions"`
	ContextWindow                 int              `json:"context_window"`
	DefaultReasoningLevel         string           `json:"default_reasoning_level"`
	DefaultReasoningSummary       string           `json:"default_reasoning_summary"`
	Description                   string           `json:"description"`
	DisplayName                   string           `json:"display_name"`
	EffectiveContextWindowPercent int              `json:"effective_context_window_percent"`
	ExperimentalSupportedTools    []any            `json:"experimental_supported_tools"`
	InputModalities               []string         `json:"input_modalities"`
	MaxContextWindow              int              `json:"max_context_window"`
	Priority                      int              `json:"priority"`
	ServiceTiers                  []any            `json:"service_tiers"`
	ShellType                     string           `json:"shell_type"`
	Slug                          string           `json:"slug"`
	SupportVerbosity              bool             `json:"support_verbosity"`
	SupportedInAPI                bool             `json:"supported_in_api"`
	SupportedReasoningLevels      []ReasoningLevel `json:"supported_reasoning_levels"`
	SupportsImageDetailOriginal   bool             `json:"supports_image_detail_original"`
	SupportsParallelToolCalls     bool             `json:"supports_parallel_tool_calls"`
	SupportsReasoningSummaries    bool             `json:"supports_reasoning_summaries"`
	SupportsSearchTool            bool             `json:"supports_search_tool"`
	TruncationPolicy              TruncationPolicy `json:"truncation_policy"`
	Upgrade                       any              `json:"upgrade"`
	Visibility                    string           `json:"visibility"`
}

// Catalog is the top-level object Codex parses.
type Catalog struct {
	Models []Entry `json:"models"`
}

// PreferredDefaults lists model ids to prefer as the Codex default, in order.
var PreferredDefaults = []string{"gpt-5.3-codex", "gpt-5.4", "primary-model"}

// Build turns a model payload into a Codex catalogue.
//
// When only is non-empty the catalogue is restricted to those model ids, in the
// order given. Unknown ids are rejected rather than silently dropped, because a
// typo there would leave Codex with an unusable catalogue.
func Build(set *upstream.ModelSet, only []string) (*Catalog, error) {
	byID := make(map[string]upstream.Model, len(set.Models))
	for _, m := range set.Models {
		byID[m.ID] = m
	}

	var selected []upstream.Model
	if len(only) > 0 {
		var unknown []string
		for _, id := range only {
			if m, ok := byID[id]; ok {
				selected = append(selected, m)
				continue
			}
			unknown = append(unknown, id)
		}
		if len(unknown) > 0 {
			return nil, fmt.Errorf("unknown model id(s): %s\navailable: %s",
				strings.Join(unknown, ", "), strings.Join(set.AllModelIDs(), ", "))
		}
	} else {
		// Without an explicit filter, drop ids Codex cannot drive (image and
		// video generators, completion-only and UI-alias entries).
		for _, m := range set.Models {
			if upstream.IsChatModel(m.ID) {
				selected = append(selected, m)
			}
		}
		// Agent-only ids are not described in models[]; synthesise usable
		// entries so they can still be selected in Codex.
		seen := make(map[string]bool, len(selected))
		for _, m := range selected {
			seen[m.ID] = true
		}
		for _, id := range set.AllModelIDs() {
			if seen[id] || !upstream.IsChatModel(id) {
				continue
			}
			seen[id] = true
			selected = append(selected, upstream.Model{
				ID:               id,
				Name:             id,
				MaxInputTokens:   128000,
				MaxOutputTokens:  32000,
				SupportsToolCall: true,
			})
		}
	}

	entries := make([]Entry, 0, len(selected))
	for i, m := range selected {
		entries = append(entries, entryFor(m, i))
	}

	// Raise the priority of the default model so Codex surfaces it first.
	if def := DefaultModel(set, only); def != "" {
		for i := range entries {
			if entries[i].Slug == def {
				entries[i].Priority += 1000
			}
		}
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Priority > entries[j].Priority
		})
	}

	return &Catalog{Models: entries}, nil
}

// entryFor maps one upstream model onto the Codex catalogue schema.
func entryFor(m upstream.Model, index int) Entry {
	contextWindow := m.MaxInputTokens
	if contextWindow <= 0 {
		contextWindow = 128000
	}
	modalities := []string{"text"}
	if m.SupportsImages {
		modalities = append(modalities, "image")
	}
	description := m.DescriptionEn
	if description == "" {
		description = m.DescriptionZh
	}
	if description == "" {
		description = m.Name
	}
	if description == "" {
		description = m.ID
	}
	displayName := m.Name
	if displayName == "" {
		displayName = m.ID
	}
	return Entry{
		AdditionalSpeedTiers:          []any{},
		AvailabilityNux:               nil,
		BaseInstructions:              BaseInstructions,
		ContextWindow:                 contextWindow,
		DefaultReasoningLevel:         "high",
		DefaultReasoningSummary:       "none",
		Description:                   description,
		DisplayName:                   displayName,
		EffectiveContextWindowPercent: 95,
		ExperimentalSupportedTools:    []any{},
		InputModalities:               modalities,
		MaxContextWindow:              contextWindow,
		Priority:                      1000 + index,
		ServiceTiers:                  []any{},
		ShellType:                     "shell_command",
		Slug:                          m.ID,
		SupportVerbosity:              false,
		SupportedInAPI:                true,
		SupportedReasoningLevels: []ReasoningLevel{
			{Description: "Disable Thinking", Effort: "none"},
			{Description: "Enabled Thinking", Effort: "high"},
		},
		SupportsImageDetailOriginal: false,
		SupportsParallelToolCalls:   m.SupportsToolCall,
		SupportsReasoningSummaries:  true,
		SupportsSearchTool:          false,
		TruncationPolicy:            TruncationPolicy{Limit: 10000, Mode: "bytes"},
		Upgrade:                     nil,
		Visibility:                  "list",
	}
}

// DefaultModel picks the most suitable model for the Codex `model` setting.
// When a filter is in force the first requested model wins outright.
func DefaultModel(set *upstream.ModelSet, only []string) string {
	if len(only) > 0 {
		return only[0]
	}
	known := make(map[string]bool, len(set.Models))
	for _, m := range set.Models {
		known[m.ID] = true
	}
	for _, candidate := range PreferredDefaults {
		if known[candidate] {
			return candidate
		}
	}
	if r := set.DefaultRelated.Reasoning; known[r] {
		return r
	}
	for _, m := range set.Models {
		if m.IsDefault {
			return m.ID
		}
	}
	if len(set.Models) > 0 {
		return set.Models[0].ID
	}
	return ""
}

// Write serialises the catalogue to ~/.codex (the bare file name Codex
// resolves via `model_catalog_json`). No intermediate copies are kept in the
// storage directory: the embedded catalogue in the repo is the source of
// truth, and this file is simply what got installed.
func Write(cat *Catalog) (codexPath string, err error) {
	pretty, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode catalog: %w", err)
	}

	codexPath, err = paths.CodexCatalog()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dirOf(codexPath), 0o755); err != nil {
		return "", fmt.Errorf("create codex dir: %w", err)
	}
	if err := fsx.AtomicWrite(codexPath, pretty, 0o644); err != nil {
		return "", fmt.Errorf("write codex catalog: %w", err)
	}
	return codexPath, nil
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '\\' || p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
