package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// parseFrames is defined in respond_test.go.

// --- custom tool downgrade ---------------------------------------------------

func TestConvertToolsDowngradesCustom(t *testing.T) {
	strict := true
	tools := []RTool{
		{Type: "function", Name: "shell", Description: "run a command", Strict: &strict},
		{Type: "custom", Name: "apply_patch", Description: "edit files", Format: json.RawMessage(`{"type":"text"}`)},
		{Type: "web_search_preview", Name: "search"},
	}
	out := convertTools(tools)
	if len(out) != 2 {
		t.Fatalf("want 2 tools (custom downgraded, web_search dropped), got %d: %v", len(out), out)
	}
	if out[0].Function.Name != "shell" {
		t.Errorf("ordinary function tool mangled: %+v", out[0])
	}
	patch := out[1]
	if patch.Function.Name != "apply_patch" {
		t.Errorf("custom tool name lost: %+v", patch)
	}
	if !strings.Contains(string(patch.Function.Parameters), `"input"`) {
		t.Errorf("downgraded tool must expose an input string parameter: %s", patch.Function.Parameters)
	}
}

func TestToChatConvertsCustomToolHistory(t *testing.T) {
	req := &RRequest{
		Model: "m",
		Input: mustRaw([]any{
			map[string]any{"type": "custom_tool_call", "call_id": "c1", "name": "apply_patch", "input": "*** Begin Patch"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": "Done!"},
			map[string]any{"type": "function_call", "call_id": "c2", "name": "shell", "arguments": `{"cmd":"ls"}`},
			map[string]any{"type": "function_call_output", "call_id": "c2", "output": "file.txt"},
		}),
	}
	cc, err := ToChat(req, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	var patchCall, shellCall *CCToolCall
	for i, m := range cc.Messages {
		for j := range m.ToolCalls {
			switch m.ToolCalls[j].Function.Name {
			case "apply_patch":
				patchCall = &cc.Messages[i].ToolCalls[j]
			case "shell":
				shellCall = &cc.Messages[i].ToolCalls[j]
			}
		}
	}
	if patchCall == nil || shellCall == nil {
		t.Fatalf("tool calls lost in conversion: %+v", cc.Messages)
	}
	var wrapper struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(patchCall.Function.Arguments), &wrapper); err != nil {
		t.Fatalf("custom call arguments not wrapped: %s", patchCall.Function.Arguments)
	}
	if wrapper.Input != "*** Begin Patch" {
		t.Errorf("patch text lost: %q", wrapper.Input)
	}
	// Both calls must have their outputs attached as tool messages.
	outputs := map[string]string{}
	for _, m := range cc.Messages {
		if m.Role == "tool" {
			outputs[m.ToolCallID] = m.Content.(string)
		}
	}
	if outputs["c1"] != "Done!" || outputs["c2"] != "file.txt" {
		t.Errorf("tool outputs mismatched: %v", outputs)
	}
}

func TestStreamCustomToolCallReWrapped(t *testing.T) {
	req := &RRequest{
		Model: "m",
		Tools: []RTool{{Type: "custom", Name: "apply_patch", Format: json.RawMessage(`{"type":"text"}`)}},
	}
	rec := httptest.NewRecorder()
	em := newEmitter(rec)
	st := NewStreamState(customToolNames(req.Tools))

	acc := &Accum{}
	var c Chunk
	chunk := `{"choices":[{"delta":{"tool_calls":[{"id":"c1","function":{"name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}}]}}]}`
	if err := json.Unmarshal([]byte(chunk), &c); err != nil {
		t.Fatal(err)
	}
	acc.add(&c)
	st.Finish(em, req, acc, "m")

	body := rec.Body.String()
	if strings.Contains(body, `"function_call"`) {
		t.Errorf("custom tool call must not be emitted as function_call:\n%s", body)
	}
	if !strings.Contains(body, `"custom_tool_call"`) {
		t.Errorf("custom_tool_call item missing:\n%s", body)
	}
	if !strings.Contains(body, "response.custom_tool_call_input.done") {
		t.Errorf("custom_tool_call_input.done event missing:\n%s", body)
	}
	if !strings.Contains(body, `"input":"*** Begin Patch"`) {
		t.Errorf("patch input not unwrapped from wrapper arguments:\n%s", body)
	}
	// Codex rejects item ids whose prefix does not match the type.
	if !strings.Contains(body, `"id":"ctc_`) {
		t.Errorf("custom tool item id must use the ctc_ prefix:\n%s", body)
	}
}

// --- response.failed ---------------------------------------------------------

func TestStreamFailureEmitsTerminalFailedEvent(t *testing.T) {
	req := &RRequest{Model: "m", Stream: true}
	rec := httptest.NewRecorder()
	em := newEmitter(rec)
	st := NewStreamState(nil)
	em.lifecycle("response.created", skeleton(st.RespID(), "m", req))

	em.lifecycle("response.failed", failedResponse(st.RespID(), "m", req,
		"upstream_stream_error", "upstream stream error; see gateway logs"))

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Errorf("response.failed event missing:\n%s", body)
	}
	if !strings.Contains(body, `"status":"failed"`) {
		t.Errorf("failed status missing:\n%s", body)
	}
	if !strings.Contains(body, `"code":"upstream_stream_error"`) {
		t.Errorf("error code missing:\n%s", body)
	}
	if !strings.Contains(body, `"type":"response.failed"`) {
		t.Errorf("frame type field missing (Codex keys off it):\n%s", body)
	}
}

func TestFailedResponseSanitisedForClient(t *testing.T) {
	// The upstream's raw error text must never reach the client payload.
	got := failedResponse("resp_1", "m", &RRequest{}, "upstream_error",
		"upstream stream error; see gateway logs")
	errObj, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("error slot missing: %v", got)
	}
	if msg, _ := errObj["message"].(string); strings.Contains(msg, "bearer") {
		t.Error("error message must not carry upstream detail")
	}
}

// --- usage -------------------------------------------------------------------

func TestConvertUsageAlwaysCarriesReasoningTokens(t *testing.T) {
	// Codex treats output_tokens_details as required; an upstream that omits
	// it must still produce the field with 0 rather than dropping it.
	out := convertUsage(&Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"output_tokens_details"`) {
		t.Errorf("output_tokens_details missing: %s", raw)
	}
	if !strings.Contains(string(raw), `"reasoning_tokens":0`) {
		t.Errorf("reasoning_tokens not pinned to 0 when absent: %s", raw)
	}
}

// --- helpers -----------------------------------------------------------------

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
