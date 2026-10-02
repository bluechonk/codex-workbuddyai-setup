// Package gateway exposes a local OpenAI Responses API endpoint that Codex can
// talk to, translating each call into a WorkBuddyAI chat-completions request.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"codex-workbuddyai-setup/internal/cred"
	"codex-workbuddyai-setup/internal/paths"
	"codex-workbuddyai-setup/internal/upstream"
)

// Server is the local proxy.
type Server struct {
	Addr         string
	DefaultModel string
	Verbose      bool
	Logger       *log.Logger
}

// NewServer builds a gateway bound to addr.
func NewServer(addr, defaultModel string, verbose bool) *Server {
	return &Server{
		Addr:         addr,
		DefaultModel: defaultModel,
		Verbose:      verbose,
		Logger:       log.New(os.Stderr, "[wbai] ", log.LstdFlags),
	}
}

// Run starts the HTTP listener and blocks until it fails.
func (s *Server) Run() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/responses", s.handleResponses)
	mux.HandleFunc("/responses", s.handleResponses)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/models", s.handleModels)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/", s.handleRoot)

	s.logf("listening on http://%s/v1/responses", s.Addr)
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}
	return srv.ListenAndServe()
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		writeJSON(w, http.StatusOK, map[string]any{
			"service": "codex-workbuddyai-setup gateway",
			"endpoints": []string{
				"/v1/responses",
				"/v1/models",
				"/healthz",
			},
		})
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "unknown path "+r.URL.Path)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	status := map[string]any{"ok": true}
	if _, err := cred.Load(); err != nil {
		status["ok"] = false
		status["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, status)
}

// handleModels lists the models known to this installation, with the
// metadata (display name, context window, modalities, reasoning levels)
// mapped from the installed catalogue.
func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	models, err := cachedModels()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", err.Error())
		return
	}
	data := make([]any, 0, len(models))
	for _, m := range models {
		entry := map[string]any{
			"id":       m.Slug,
			"object":   "model",
			"created":  time.Now().Unix(),
			"owned_by": "workbuddyai",
			"name":     m.DisplayName,
		}
		if m.Description != "" {
			entry["description"] = m.Description
		}
		if m.ContextWindow > 0 {
			entry["context_window"] = m.ContextWindow
		}
		if len(m.InputModalities) > 0 {
			entry["input_modalities"] = m.InputModalities
		}
		entry["output_modalities"] = []string{"text"}
		if len(m.ReasoningLevels) > 0 {
			entry["effort"] = map[string]any{
				"supported_levels": m.ReasoningLevels,
				"default_level":    m.DefaultReasoningLevel,
			}
		}
		data = append(data, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// handleResponses serves POST /v1/responses.
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "cannot read body: "+err.Error())
		return
	}
	var req RRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	ccReq, err := ToChat(&req, s.DefaultModel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.Verbose {
		s.logf("model=%s stream=%v messages=%d tools=%d", ccReq.Model, req.Stream, len(ccReq.Messages), len(ccReq.Tools))
	}

	cfg, c, err := s.credentials()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_authenticated", err.Error())
		return
	}

	ctx := r.Context()
	resp, err := openStream(ctx, cfg, c.AccessToken, c.UID, ccReq)
	if errors.Is(err, ErrUpstreamUnauthorized) {
		s.logf("token rejected; attempting refresh")
		if refreshed, refreshErr := cred.Refresh(c); refreshErr == nil {
			c = refreshed
			resp, err = openStream(ctx, cfg, c.AccessToken, c.UID, ccReq)
		} else {
			s.logf("refresh failed: %v", refreshErr)
		}
	}
	if err != nil {
		s.logf("upstream request error: %v", err)
		if errors.Is(err, ErrUpstreamUnauthorized) {
			writeError(w, http.StatusBadGateway, "upstream_unauthorized",
				"WorkBuddyAI rejected the token and refresh failed; run `wbai login`")
			return
		}
		writeError(w, http.StatusBadGateway, "upstream_error", "upstream request failed; see gateway logs")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if req.Stream {
		s.writeStream(w, ctx, &req, ccReq, resp.Body)
		return
	}

	acc := &Accum{}
	if err := consumeStream(ctx, resp.Body, acc, nil); err != nil {
		s.logf("upstream stream error: %v", err)
		writeError(w, http.StatusBadGateway, "upstream_stream_error", "upstream stream error; see gateway logs")
		return
	}
	out := BuildResponse(&req, acc, ccReq.Model, nil)
	writeJSON(w, http.StatusOK, out)
}

// writeStream emits the Responses SSE sequence for one turn.
func (s *Server) writeStream(w http.ResponseWriter, ctx context.Context, req *RRequest, ccReq *CCRequest, body io.Reader) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	em := newEmitter(w)
	st := NewStreamState(customToolNames(req.Tools))
	em.lifecycle("response.created", skeleton(st.RespID(), ccReq.Model, req))

	acc := &Accum{}
	if err := consumeStream(ctx, body, acc, st.ChunkHandler(em)); err != nil {
		// Full detail goes to the server log only; the client gets a
		// classified message plus the terminal response.failed event so it
		// does not wait on a stream that is already dead.
		s.logf("upstream stream error: %v", err)
		em.lifecycle("response.failed", failedResponse(st.RespID(), ccReq.Model, req,
			"upstream_stream_error", "upstream stream error; see gateway logs"))
		return
	}
	st.Finish(em, req, acc, ccReq.Model)
}

// credentials loads the upstream config and the stored token.
func (s *Server) credentials() (upstream.Config, *cred.Credentials, error) {
	cfg, err := upstream.LoadConfig()
	if err != nil && !errors.Is(err, upstream.ErrNoUpstream) {
		return cfg, nil, err
	}
	c, err := cred.Load()
	if err != nil {
		return cfg, nil, err
	}
	if c.Domain != "" && c.Domain != hostOf(cfg.BaseURL) {
		cfg.BaseURL = "https://" + c.Domain
	}
	return cfg, c, nil
}

// catalogModel is the subset of catalogue fields the /v1/models endpoint
// exposes. The catalogue is the richer of the two sources (desktop-app
// cache merged with the CLI API), so the endpoint mirrors it.
type catalogModel struct {
	Slug                  string   `json:"slug"`
	DisplayName           string   `json:"display_name"`
	Description           string   `json:"description"`
	ContextWindow         int      `json:"context_window"`
	InputModalities       []string `json:"input_modalities"`
	DefaultReasoningLevel string   `json:"default_reasoning_level"`
	ReasoningLevels       []string `json:"-"`
}

// cachedModels reads the installed catalogue (~/.codex/workbuddyai-models.json)
// into the endpoint-facing shape.
func cachedModels() ([]catalogModel, error) {
	path, err := paths.CodexCatalog()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	var cat struct {
		Models []struct {
			catalogModel
			Levels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	var models []catalogModel
	for _, m := range cat.Models {
		if m.Slug == "" {
			continue
		}
		cm := m.catalogModel
		for _, lv := range m.Levels {
			if lv.Effort != "" {
				cm.ReasoningLevels = append(cm.ReasoningLevels, lv.Effort)
			}
		}
		models = append(models, cm)
	}
	if len(models) == 0 {
		return nil, errors.New("catalog is empty or missing; run `wbai setup`")
	}
	return models, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"type":    "error",
			"code":    code,
			"message": message,
		},
	})
}
