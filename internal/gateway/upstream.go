package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"codex-workbuddyai-setup/internal/upstream"
)

// ErrUpstreamUnauthorized means the upstream rejected our access token.
var ErrUpstreamUnauthorized = errors.New("upstream returned 401/403")

// ToolCall is one fully assembled function call from the upstream stream.
type ToolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// Accum aggregates an upstream SSE stream into a complete assistant turn.
type Accum struct {
	ID           string
	Model        string
	Created      int64
	Content      strings.Builder
	Reasoning    strings.Builder
	Tools        []*toolAcc
	FinishReason string
	Usage        *Usage
	firstChunk   bool
}

type toolAcc struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

// ToolCalls returns the assembled calls in emission order.
func (a *Accum) ToolCalls() []ToolCall {
	out := make([]ToolCall, 0, len(a.Tools))
	for _, t := range a.Tools {
		args := t.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		out = append(out, ToolCall{Index: t.index, ID: t.id, Name: t.name, Arguments: args})
	}
	return out
}

// add folds a single upstream chunk into the accumulator.
func (a *Accum) add(c *Chunk) {
	if a.ID == "" && c.ID != "" {
		a.ID = c.ID
	}
	if a.Model == "" && c.Model != "" {
		a.Model = c.Model
	}
	if a.Created == 0 && c.Created != 0 {
		a.Created = c.Created
	}
	for _, choice := range c.Choices {
		a.Content.WriteString(choice.Delta.Content)
		a.Reasoning.WriteString(choice.Delta.ReasoningContent)
		for _, d := range choice.Delta.ToolCalls {
			t := a.toolFor(d)
			if t == nil {
				continue
			}
			if d.ID != "" {
				t.id = d.ID
			}
			if d.Function.Name != "" {
				t.name = d.Function.Name
			}
			t.args.WriteString(d.Function.Arguments)
		}
		if choice.FinishReason != "" {
			a.FinishReason = choice.FinishReason
		}
	}
	if c.Usage != nil {
		a.Usage = c.Usage
	}
}

// toolFor resolves which accumulated call a delta belongs to.
func (a *Accum) toolFor(d CCToolCallDelta) *toolAcc {
	if d.Index != nil {
		idx := *d.Index
		for _, t := range a.Tools {
			if t.index == idx {
				return t
			}
		}
		t := &toolAcc{index: idx, id: d.ID}
		a.Tools = append(a.Tools, t)
		return t
	}
	if d.ID != "" {
		for _, t := range a.Tools {
			if t.id == d.ID {
				return t
			}
		}
	}
	if len(a.Tools) > 0 && d.Function.Name == "" {
		return a.Tools[len(a.Tools)-1]
	}
	t := &toolAcc{index: len(a.Tools), id: d.ID}
	a.Tools = append(a.Tools, t)
	return t
}

// StreamChat posts a chat request and consumes the SSE response.
//
// The upstream only supports streaming, so every request is sent with
// stream=true and callers decide whether to aggregate or forward deltas.
// onChunk (when non-nil) receives each chunk as it arrives.
func StreamChat(ctx context.Context, cfg upstream.Config, token, uid string, req *CCRequest, onChunk func(*Chunk)) (*Accum, error) {
	resp, err := openStream(ctx, cfg, token, uid, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	acc := &Accum{}
	if err := consumeStream(ctx, resp.Body, acc, onChunk); err != nil {
		return nil, err
	}
	return acc, nil
}

// openStream issues the upstream request and validates the status code before
// any bytes are forwarded to Codex, so authentication failures can still be
// retried with a refreshed token.
func openStream(ctx context.Context, cfg upstream.Config, token, uid string, req *CCRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode upstream request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ChatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyAuth(httpReq, cfg, token, uid)

	// Timeouts mirroring CodexPlusPlus's tiers: a slow upstream must not hang
	// the request forever, but long streaming turns must not be truncated
	// either — hence no overall client timeout, only connection and
	// response-header deadlines.
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 120 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrUpstreamUnauthorized, strings.TrimSpace(string(snippet)))
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("upstream returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return resp, nil
}

// consumeStream folds the SSE body into acc, invoking onChunk per chunk.
func consumeStream(ctx context.Context, r io.Reader, acc *Accum, onChunk func(*Chunk)) error {
	reader := bufio.NewReaderSize(r, 256*1024)
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed != "" {
			if payload, ok := strings.CutPrefix(trimmed, "data:"); ok {
				data := strings.TrimSpace(payload)
				if data != "" && data != "[DONE]" {
					var c Chunk
					if json.Unmarshal([]byte(data), &c) == nil {
						acc.add(&c)
						if onChunk != nil {
							onChunk(&c)
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read upstream stream: %w", err)
		}
	}
}

// applyAuth sets the authentication headers advertised by the model payload.
func applyAuth(req *http.Request, cfg upstream.Config, token, uid string) {
	header := cfg.TokenHeader
	if header == "" {
		header = "Authorization"
	}
	req.Header.Set(header, "Bearer "+token)

	if cfg.UsernameHeader != "" && uid != "" {
		req.Header.Set(cfg.UsernameHeader, uid)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "CodeBuddyCode/1.0")
	req.Header.Set("X-Domain", hostOf(cfg.BaseURL))
}

func hostOf(baseURL string) string {
	s := baseURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	return s
}
