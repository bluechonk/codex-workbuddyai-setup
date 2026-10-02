package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FallbackSystem is injected when Codex sends no instructions at all: the
// upstream rejects any request whose first message is not a system prompt.
const FallbackSystem = "You are Codex, a coding agent. You and the user share the same workspace and collaborate to achieve the user's goals."

// ToChat converts a Responses request into an upstream chat-completions
// request. The upstream always receives stream=true; non-streaming callers
// are served by aggregating the stream inside the gateway.
func ToChat(req *RRequest, defaultModel string) (*CCRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("empty request")
	}

	model := req.Model
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("no model specified")
	}

	systemText := instructionText(req.Instructions)

	var messages []CCMessage
	var pending []CCToolCall

	flush := func() {
		if len(pending) == 0 {
			return
		}
		messages = append(messages, CCMessage{Role: "assistant", Content: "", ToolCalls: pending})
		pending = nil
	}

	for _, item := range inputItems(req.Input) {
		switch item.Type {
		case "message":
			role := item.Role
			switch role {
			case "system", "developer":
				text := partsText(item.Content)
				if text != "" {
					if systemText != "" {
						systemText += "\n\n"
					}
					systemText += text
				}
				continue
			case "assistant":
				flush()
				messages = append(messages, CCMessage{Role: "assistant", Content: partsText(item.Content)})
				continue
			default:
				flush()
				content := userContent(item.Content)
				if content == nil {
					continue
				}
				messages = append(messages, CCMessage{Role: "user", Content: content})
			}

		case "function_call":
			args := item.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			if callID == "" {
				callID = newID("call")
			}
			pending = append(pending, CCToolCall{
				ID:       callID,
				Type:     "function",
				Function: CCToolCallFunc{Name: item.Name, Arguments: args},
			})

		case "function_call_output":
			flush()
			messages = append(messages, CCMessage{
				Role:       "tool",
				Content:    outputText(item.Output),
				ToolCallID: item.CallID,
			})

		case "custom_tool_call":
			// A downgraded freeform tool call coming back in history: the
			// patch text lives in `input`; re-wrap it the way convertTools
			// packaged the tool declaration so the upstream sees a normal
			// function call.
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			if callID == "" {
				callID = newID("call")
			}
			pending = append(pending, CCToolCall{
				ID:       callID,
				Type:     "function",
				Function: CCToolCallFunc{Name: item.Name, Arguments: wrapCustomInput(item.Input)},
			})

		case "custom_tool_call_output":
			flush()
			messages = append(messages, CCMessage{
				Role:       "tool",
				Content:    outputText(item.Output),
				ToolCallID: item.CallID,
			})

		default:
			// reasoning, item_reference, computer_call, web_search_call and
			// friends carry no information the chat endpoint understands.
			continue
		}
	}
	flush()

	if systemText == "" {
		systemText = FallbackSystem
	}
	messages = append([]CCMessage{{Role: "system", Content: systemText}}, messages...)
	messages = mergeSameRole(messages)

	out := &CCRequest{
		Model:          model,
		Messages:       messages,
		Stream:         true,
		StreamOptions:  &CCStreamOptions{IncludeUsage: true},
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		MaxTokens:      req.MaxOutputTokens,
		ToolChoice:     convertToolChoice(req.ToolChoice),
		Tools:          convertTools(req.Tools),
		ResponseFormat: convertTextFormat(req.Text),
	}
	if req.ParallelToolCalls != nil {
		v := *req.ParallelToolCalls
		out.ParallelToolCall = &v
	}
	if effort := reasoningEffort(req.Reasoning); effort != "" {
		out.ReasoningEffort = effort
	}
	return out, nil
}

// inputItems normalises `input` into a slice: it may be a bare string.
func inputItems(raw json.RawMessage) []RItem {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []RItem{{
			Type:    "message",
			Role:    "user",
			Content: mustMarshal(s),
		}}
	}
	var items []RItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	return items
}

// instructionText flattens `instructions` (string or content-part array).
func instructionText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// contentPart is a subset of the Responses content-part union.
type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
	Image    string `json:"image"`
	FileURL  string `json:"file_url"`
	FileData string `json:"file_data"`
	FileID   string `json:"file_id"`
}

// partsText concatenates every textual part of a content array or string.
func partsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

// userContent builds the user message content: a plain string when the message
// is text-only, otherwise an array of chat-completions content parts.
func userContent(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return s
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}

	var out []any
	var text strings.Builder
	flushText := func() {
		if text.Len() == 0 {
			return
		}
		out = append(out, map[string]any{"type": "text", "text": text.String()})
		text.Reset()
	}

	for _, p := range parts {
		switch p.Type {
		case "input_text", "text", "output_text", "":
			if p.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(p.Text)
			}
		case "input_image":
			url := p.ImageURL
			if url == "" {
				url = p.Image
			}
			if url == "" {
				continue
			}
			flushText()
			out = append(out, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		case "input_file":
			// The chat endpoint does not accept binary uploads; inline what we
			// can and skip the rest rather than failing the whole request.
			if p.FileURL != "" {
				flushText()
				out = append(out, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": p.FileURL},
				})
			}
		}
	}
	flushText()

	if len(out) == 0 {
		return nil
	}
	if len(out) == 1 {
		if m, ok := out[0].(map[string]any); ok && m["type"] == "text" {
			return m["text"]
		}
	}
	return out
}

// outputText flattens a function_call_output payload into a string.
func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		return partsText(raw)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return ""
}

// convertTools maps Responses tools onto chat-completions function tools.
// Custom (freeform) tools — Codex's apply_patch among them — cannot be passed
// through, so they are downgraded to a function whose single `input` string
// parameter carries the original freeform payload; the response side unwraps
// it back into a custom_tool_call item.
func convertTools(tools []RTool) []CCTool {
	var out []CCTool
	for _, t := range tools {
		if t.Type != "" && t.Type != "function" && t.Type != "custom" {
			// web_search_preview, mcp... are unsupported upstream.
			continue
		}
		if t.Name == "" {
			continue
		}
		params := t.Parameters
		if t.Type == "custom" {
			params = json.RawMessage(customToolParams)
		}
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, CCTool{
			Type: "function",
			Function: CCToolFnSpec{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

// customToolParams is the function schema a custom tool is downgraded to.
const customToolParams = `{"type":"object","properties":{"input":{"type":"string","description":"The full freeform tool payload (e.g. the apply_patch text)."}},"required":["input"]}`

// customToolNames returns the names of the tools that were downgraded, so the
// response side knows which function calls to re-wrap as custom_tool_call.
func customToolNames(tools []RTool) map[string]bool {
	out := map[string]bool{}
	for _, t := range tools {
		if t.Type == "custom" && t.Name != "" {
			out[t.Name] = true
		}
	}
	return out
}

// wrapCustomInput packs a freeform payload into the wrapper arguments shape.
func wrapCustomInput(input string) string {
	b, err := json.Marshal(map[string]string{"input": input})
	if err != nil {
		return "{}"
	}
	return string(b)
}

// unwrapCustomInput extracts the freeform payload from wrapper arguments.
// When the arguments are not in the expected shape (the model answered with
// freeform text directly), they are returned verbatim so nothing is lost.
func unwrapCustomInput(args string) string {
	var v struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(args), &v); err == nil {
		return v.Input
	}
	return args
}

// convertToolChoice maps Responses tool_choice onto the chat-completions form.
func convertToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto", "none", "required":
			return s
		default:
			return nil
		}
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	switch obj.Type {
	case "function":
		if obj.Name == "" {
			return nil
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": obj.Name},
		}
	case "allowed_tools", "tool":
		return "auto"
	default:
		return nil
	}
}

// reasoningEffort extracts `reasoning.effort`.
func reasoningEffort(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	switch obj.Effort {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return obj.Effort
	}
	return ""
}

// convertTextFormat maps `text.format` onto response_format when it asks for
// structured JSON output.
func convertTextFormat(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var obj struct {
		Format struct {
			Type   string          `json:"type"`
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict *bool           `json:"strict"`
		} `json:"format"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	switch obj.Format.Type {
	case "json_schema":
		if len(obj.Format.Schema) == 0 {
			return nil
		}
		strict := true
		if obj.Format.Strict != nil {
			strict = *obj.Format.Strict
		}
		name := obj.Format.Name
		if name == "" {
			name = "response"
		}
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"schema": obj.Format.Schema,
				"strict": strict,
			},
		}
	case "json_object":
		return map[string]any{"type": "json_object"}
	default:
		return nil
	}
}

// mergeSameRole collapses consecutive plain-text messages of the same role so
// the upstream never sees a malformed message sequence.
func mergeSameRole(in []CCMessage) []CCMessage {
	var out []CCMessage
	for _, m := range in {
		if len(out) > 0 && len(m.ToolCalls) == 0 && len(out[len(out)-1].ToolCalls) == 0 &&
			out[len(out)-1].Role == m.Role && m.ToolCallID == "" && out[len(out)-1].ToolCallID == "" {
			prev, ok := out[len(out)-1].Content.(string)
			cur, ok2 := m.Content.(string)
			if ok && ok2 {
				out[len(out)-1].Content = joinNonEmpty(prev, cur)
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}
