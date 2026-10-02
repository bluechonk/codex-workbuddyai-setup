package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// parseFrames splits an SSE body into (eventName, data) pairs.
func parseFrames(t *testing.T, body string) []struct {
	event string
	data  map[string]any
} {
	t.Helper()
	var out []struct {
		event string
		data  map[string]any
	}
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var event string
		var dataLine string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				dataLine = strings.TrimPrefix(line, "data: ")
			}
		}
		if event == "" || dataLine == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(dataLine), &m); err != nil {
			t.Fatalf("event %s: data is not JSON: %v", event, err)
		}
		out = append(out, struct {
			event string
			data  map[string]any
		}{event, m})
	}
	return out
}

// TestEveryFrameCarriesType guards the wire contract: Codex dispatches on the
// payload's `type` field, so a frame without it is dropped.
func TestEveryFrameCarriesType(t *testing.T) {
	rec := httptest.NewRecorder()
	em := newEmitter(rec)

	em.lifecycle("response.created", skeleton("resp_1", "hy4-preview-f", &RRequest{}))
	em.send("response.output_item.added", map[string]any{
		"item_id": "msg_1", "output_index": 0,
		"item": map[string]any{"id": "msg_1", "type": "message", "role": "assistant"},
	})
	em.send("response.output_text.delta", map[string]any{
		"item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": "hi",
	})
	em.send("response.output_item.done", map[string]any{"item_id": "msg_1", "output_index": 0})
	em.lifecycle("response.completed", responsePayload(&RResponse{
		ID: "resp_1", Object: "response", Status: "completed", Output: []any{},
	}))

	frames := parseFrames(t, rec.Body.String())
	if len(frames) != 5 {
		t.Fatalf("expected 5 frames, got %d\n%s", len(frames), rec.Body.String())
	}
	for i, f := range frames {
		got, _ := f.data["type"].(string)
		if got != f.event {
			t.Errorf("frame %d (%s): payload type=%q, want it to match the event name", i, f.event, got)
		}
		if _, ok := f.data["sequence_number"]; !ok {
			t.Errorf("frame %d (%s): missing sequence_number", i, f.event)
		}
	}
}

// TestLifecycleFramesWrapResponse checks that response.created and
// response.completed expose the Response object both flattened and nested.
func TestLifecycleFramesWrapResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	em := newEmitter(rec)
	em.lifecycle("response.created", skeleton("resp_9", "deepseek-v4.1-flash", &RRequest{}))

	frames := parseFrames(t, rec.Body.String())
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	data := frames[0].data
	if data["id"] != "resp_9" {
		t.Errorf("flattened id missing/wrong: %v", data["id"])
	}
	nested, ok := data["response"].(map[string]any)
	if !ok {
		t.Fatalf("missing nested `response` object")
	}
	if nested["id"] != "resp_9" {
		t.Errorf("nested id wrong: %v", nested["id"])
	}
	if nested["status"] != "in_progress" {
		t.Errorf("nested status wrong: %v", nested["status"])
	}
}

// TestNoDoneSentinel makes sure we never emit the chat-completions `[DONE]`
// sentinel, which is not part of the Responses stream format.
func TestNoDoneSentinel(t *testing.T) {
	rec := httptest.NewRecorder()
	em := newEmitter(rec)
	em.lifecycle("response.completed", responsePayload(&RResponse{ID: "resp_1", Object: "response"}))
	if strings.Contains(rec.Body.String(), "[DONE]") {
		t.Error("stream must not contain the [DONE] sentinel")
	}
}

// TestBuildResponseOutputShape covers the non-streaming payload Codex parses.
func TestBuildResponseOutputShape(t *testing.T) {
	acc := &Accum{}
	acc.Content.WriteString("hello")
	req := &RRequest{}
	resp := BuildResponse(req, acc, "hy4-preview-f", nil)

	if resp.Object != "response" {
		t.Errorf("object=%q, want response", resp.Object)
	}
	if resp.Status != "completed" {
		t.Errorf("status=%q, want completed", resp.Status)
	}
	if resp.OutputText != "hello" {
		t.Errorf("output_text=%q, want hello", resp.OutputText)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("output len=%d, want 1", len(resp.Output))
	}
	item, _ := resp.Output[0].(map[string]any)
	if item["type"] != "message" {
		t.Errorf("item type=%v, want message", item["type"])
	}
	content, _ := item["content"].([]any)
	part, _ := content[0].(map[string]any)
	if part["type"] != "output_text" {
		t.Errorf("part type=%v, want output_text", part["type"])
	}
}
