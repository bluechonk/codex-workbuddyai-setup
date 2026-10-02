package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// itemIDs keeps output-item identifiers stable between the SSE stream and the
// final response.completed payload.
type itemIDs struct {
	resp      string
	reasoning string
	message   string
	calls     []string
}

func newItemIDs(acc *Accum, custom map[string]bool) *itemIDs {
	ids := &itemIDs{resp: newID("resp"), reasoning: newID("rs"), message: newID("msg")}
	for _, c := range acc.ToolCalls() {
		prefix := "fc"
		if custom[c.Name] {
			prefix = "ctc"
		}
		ids.calls = append(ids.calls, newID(prefix))
	}
	return ids
}

// BuildResponse turns an aggregated upstream turn into a Responses object.
// Pass ids to reuse identifiers already announced on the SSE stream.
func BuildResponse(req *RRequest, acc *Accum, model string, ids *itemIDs) *RResponse {
	if ids == nil {
		ids = newItemIDs(acc, customToolNames(req.Tools))
	}
	created := acc.Created
	if created == 0 {
		created = time.Now().Unix()
	}

	text := acc.Content.String()
	calls := acc.ToolCalls()

	var output []any
	if reasoning := acc.Reasoning.String(); reasoning != "" {
		output = append(output, map[string]any{
			"id":     ids.reasoning,
			"type":   "reasoning",
			"status": "completed",
			"summary": []any{
				map[string]any{"type": "summary_text", "text": reasoning},
			},
		})
	}
	if text != "" || len(calls) == 0 {
		output = append(output, map[string]any{
			"id":     ids.message,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []any{
				map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
			},
		})
	}
	custom := customToolNames(req.Tools)
	for i, call := range calls {
		id := newID(toolIDPrefix(call.Name, custom))
		if i < len(ids.calls) {
			id = ids.calls[i]
		}
		if custom[call.Name] {
			output = append(output, map[string]any{
				"id":      id,
				"type":    "custom_tool_call",
				"call_id": call.ID,
				"name":    call.Name,
				"input":   unwrapCustomInput(call.Arguments),
				"status":  "completed",
			})
			continue
		}
		output = append(output, map[string]any{
			"id":        id,
			"type":      "function_call",
			"call_id":   call.ID,
			"name":      call.Name,
			"arguments": call.Arguments,
			"status":    "completed",
		})
	}
	if output == nil {
		output = []any{}
	}

	status := "completed"
	var incomplete any
	switch acc.FinishReason {
	case "length":
		status = "incomplete"
		incomplete = map[string]any{"reason": "max_output_tokens"}
	case "content_filter":
		status = "incomplete"
		incomplete = map[string]any{"reason": "content_filter"}
	}

	resp := &RResponse{
		ID:                ids.resp,
		Object:            "response",
		CreatedAt:         created,
		Status:            status,
		Model:             model,
		Output:            output,
		OutputText:        text,
		Error:             nil,
		IncompleteDetails: incomplete,
		ParallelToolCalls: req.ParallelToolCalls == nil || *req.ParallelToolCalls,
		ToolChoice:        "auto",
		Tools:             echoTools(req.Tools),
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		MaxOutputTokens:   req.MaxOutputTokens,
		Metadata:          req.Metadata,
		Truncation:        req.Truncation,
	}
	if len(req.Instructions) > 0 {
		resp.Instructions = req.Instructions
	}
	if len(req.Reasoning) > 0 {
		resp.Reasoning = req.Reasoning
	}
	if len(req.Text) > 0 {
		resp.Text = req.Text
	}
	if acc.Usage != nil {
		resp.Usage = convertUsage(acc.Usage)
	}
	return resp
}

// convertUsage maps chat-completions usage onto the Responses usage object.
func convertUsage(u *Usage) *RUsage {
	out := &RUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
	}
	out.InputDetail.CachedTokens = u.PromptDetail.CachedTokens
	out.OutputDetail.ReasoningTokens = u.CompletionDetail.ReasoningTokens
	return out
}

func echoTools(tools []RTool) []any {
	if len(tools) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type":        orDefault(t.Type, "function"),
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
			"strict":      t.Strict != nil && *t.Strict,
		})
	}
	return out
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

// emitter writes Server-Sent Events in the Responses API event format.
//
// Every frame carries the event name in `type` and a monotonically increasing
// `sequence_number`, which is what the OpenAI Responses wire format specifies.
// Clients that key off `type` rather than the SSE `event:` line (Codex among
// them) silently drop frames without it.
type emitter struct {
	w     http.ResponseWriter
	flush func()
	seq   int
}

func newEmitter(w http.ResponseWriter) *emitter {
	flush := func() {}
	if f, ok := w.(http.Flusher); ok {
		flush = f.Flush
	}
	return &emitter{w: w, flush: flush}
}

// send writes one `event: <name>` frame followed by its JSON payload.
func (e *emitter) send(name string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["type"] = name
	payload["sequence_number"] = e.seq
	e.seq++

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(name)
	b.WriteString("\ndata: ")
	b.WriteString(string(data))
	b.WriteString("\n\n")
	_, _ = e.w.Write([]byte(b.String()))
	e.flush()
}

// lifecycle emits an event whose payload is a whole Response object.
// The object is sent both flattened and under `response`, so clients written
// against either shape can read it.
func (e *emitter) lifecycle(name string, resp map[string]any) {
	if resp == nil {
		resp = map[string]any{}
	}
	frame := make(map[string]any, len(resp)+2)
	for k, v := range resp {
		frame[k] = v
	}
	frame["response"] = resp
	e.send(name, frame)
}

// responsePayload flattens a built Response object into an event payload.
func responsePayload(resp *RResponse) map[string]any {
	raw, err := json.Marshal(resp)
	if err != nil {
		return map[string]any{"object": "response"}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"object": "response"}
	}
	return m
}

// streamState tracks which output items have been announced on the wire.
type streamState struct {
	ids       *itemIDs
	outIndex  int
	reasoning *itemState
	message   *itemState
	partOpen  bool
	text      strings.Builder
	resnText  strings.Builder
	tools     map[int]*toolStream
	toolOrder []int
	// custom holds the names of tools that were downgraded from freeform
	// custom tools; their calls are re-wrapped as custom_tool_call items.
	custom map[string]bool
}

type itemState struct {
	id   string
	open bool
}

type toolStream struct {
	itemID   string
	outIndex int
	name     string
	callID   string
	args     strings.Builder
	added    bool
	done     bool
	custom   bool
}

// itemType is the Responses output-item type this call is re-wrapped as.
func (ts *toolStream) itemType() string {
	if ts.custom {
		return "custom_tool_call"
	}
	return "function_call"
}

// argField is the payload field carrying the call payload (`input` for
// custom_tool_call, `arguments` for function_call).
func (ts *toolStream) argField() string {
	if ts.custom {
		return "input"
	}
	return "arguments"
}

// deltaEvent / doneEvent are the streaming event names for this call kind.
func (ts *toolStream) deltaEvent() string {
	if ts.custom {
		return "response.custom_tool_call_input.delta"
	}
	return "response.function_call_arguments.delta"
}

func (ts *toolStream) doneEvent() string {
	if ts.custom {
		return "response.custom_tool_call_input.done"
	}
	return "response.function_call_arguments.done"
}

// payloadText returns the call payload in wire form for the item type.
func (ts *toolStream) payloadText(rawArgs string) string {
	if ts.custom {
		return unwrapCustomInput(rawArgs)
	}
	return rawArgs
}

// NewStreamState prepares the state machine for one streamed turn. custom is
// the set of downgraded custom-tool names (see convertTools).
func NewStreamState(custom map[string]bool) *streamState {
	return &streamState{
		ids:    &itemIDs{resp: newID("resp")},
		tools:  map[int]*toolStream{},
		custom: custom,
	}
}

// RespID is the identifier announced in response.created.
func (st *streamState) RespID() string { return st.ids.resp }

// ChunkHandler adapts the state machine to the StreamChat callback signature.
func (st *streamState) ChunkHandler(em *emitter) func(*Chunk) {
	return func(c *Chunk) { st.onChunk(em, c) }
}

// onChunk forwards one upstream chunk as Responses events.
func (st *streamState) onChunk(em *emitter, c *Chunk) {
	for _, choice := range c.Choices {
		delta := choice.Delta
		if delta.ReasoningContent != "" {
			st.resnText.WriteString(delta.ReasoningContent)
			st.openReasoning(em)
			em.send("response.reasoning_summary_text.delta", map[string]any{
				"item_id":       st.reasoning.id,
				"output_index":  st.outIndex,
				"summary_index": 0,
				"delta":         delta.ReasoningContent,
			})
		}
		if delta.Content != "" {
			st.text.WriteString(delta.Content)
			st.closeReasoning(em)
			st.openMessage(em)
			em.send("response.output_text.delta", map[string]any{
				"item_id":       st.message.id,
				"output_index":  st.outIndex,
				"content_index": 0,
				"delta":         delta.Content,
			})
		}
		for _, d := range delta.ToolCalls {
			st.closeReasoning(em)
			st.closeMessage(em)
			ts := st.toolFor(d)
			if !ts.added {
				ts.added = true
				ts.outIndex = st.outIndex
				st.outIndex++
				em.send("response.output_item.added", map[string]any{
					"item_id":      ts.itemID,
					"output_index": ts.outIndex,
					"item": map[string]any{
						"id":          ts.itemID,
						"type":        ts.itemType(),
						"call_id":     ts.callID,
						"name":        ts.name,
						ts.argField(): "",
						"status":      "in_progress",
					},
				})
			}
			if d.Function.Arguments != "" {
				ts.args.WriteString(d.Function.Arguments)
				em.send(ts.deltaEvent(), map[string]any{
					"item_id":      ts.itemID,
					"output_index": ts.outIndex,
					"delta":        ts.payloadText(d.Function.Arguments),
				})
			}
		}
	}
}

// Finish closes every open item and emits the terminal response.completed.
func (st *streamState) Finish(em *emitter, req *RRequest, acc *Accum, model string) {
	st.closeReasoning(em)
	st.closeMessage(em)
	st.finishTools(em, acc)

	st.ids.reasoning = idOf(st.reasoning)
	st.ids.message = idOf(st.message)
	st.ids.calls = nil
	for _, idx := range st.toolOrder {
		if ts := st.tools[idx]; ts != nil {
			st.ids.calls = append(st.ids.calls, ts.itemID)
		}
	}

	em.lifecycle("response.completed", responsePayload(BuildResponse(req, acc, model, st.ids)))
}

func (st *streamState) openReasoning(em *emitter) {
	if st.reasoning != nil {
		return
	}
	id := newID("rs")
	st.reasoning = &itemState{id: id, open: true}
	em.send("response.output_item.added", map[string]any{
		"item_id":      id,
		"output_index": st.outIndex,
		"item": map[string]any{
			"id":      id,
			"type":    "reasoning",
			"status":  "in_progress",
			"summary": []any{},
		},
	})
}

func (st *streamState) closeReasoning(em *emitter) {
	if st.reasoning == nil || !st.reasoning.open {
		return
	}
	text := st.resnText.String()
	em.send("response.reasoning_summary_text.done", map[string]any{
		"item_id":       st.reasoning.id,
		"output_index":  st.outIndex,
		"summary_index": 0,
		"text":          text,
	})
	em.send("response.output_item.done", map[string]any{
		"item_id":      st.reasoning.id,
		"output_index": st.outIndex,
		"item": map[string]any{
			"id":      st.reasoning.id,
			"type":    "reasoning",
			"status":  "completed",
			"summary": []any{map[string]any{"type": "summary_text", "text": text}},
		},
	})
	st.reasoning.open = false
	st.outIndex++
}

func (st *streamState) openMessage(em *emitter) {
	if st.message == nil {
		id := newID("msg")
		st.message = &itemState{id: id, open: true}
		em.send("response.output_item.added", map[string]any{
			"item_id":      id,
			"output_index": st.outIndex,
			"item": map[string]any{
				"id":      id,
				"type":    "message",
				"role":    "assistant",
				"status":  "in_progress",
				"content": []any{},
			},
		})
	}
	if !st.partOpen {
		st.partOpen = true
		em.send("response.content_part.added", map[string]any{
			"item_id":       st.message.id,
			"output_index":  st.outIndex,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})
	}
}

func (st *streamState) closeMessage(em *emitter) {
	if st.message == nil || !st.message.open {
		return
	}
	text := st.text.String()
	if st.partOpen {
		em.send("response.output_text.done", map[string]any{
			"item_id":       st.message.id,
			"output_index":  st.outIndex,
			"content_index": 0,
			"text":          text,
		})
		em.send("response.content_part.done", map[string]any{
			"item_id":       st.message.id,
			"output_index":  st.outIndex,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			},
		})
		st.partOpen = false
	}
	em.send("response.output_item.done", map[string]any{
		"item_id":      st.message.id,
		"output_index": st.outIndex,
		"item": map[string]any{
			"id":     st.message.id,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []any{
				map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
			},
		},
	})
	st.message.open = false
	st.outIndex++
}

func (st *streamState) finishTools(em *emitter, acc *Accum) {
	for _, call := range acc.ToolCalls() {
		ts := st.lookup(call)
		if ts == nil {
			// The call never streamed a delta (e.g. it arrived whole in one
			// chunk we did not see); create its slot now so the item is not
			// silently dropped.
			ts = &toolStream{
				itemID: newID(toolIDPrefix(call.Name, st.custom)),
				name:   call.Name,
				callID: call.ID,
				custom: st.custom[call.Name],
			}
			st.tools[call.Index] = ts
			st.toolOrder = append(st.toolOrder, call.Index)
		}
		if ts.done {
			continue
		}
		if !ts.added {
			ts.added = true
			ts.outIndex = st.outIndex
			st.outIndex++
			em.send("response.output_item.added", map[string]any{
				"item_id":      ts.itemID,
				"output_index": ts.outIndex,
				"item": map[string]any{
					"id":          ts.itemID,
					"type":        ts.itemType(),
					"call_id":     call.ID,
					"name":        call.Name,
					ts.argField(): "",
					"status":      "in_progress",
				},
			})
			if call.Arguments != "" && call.Arguments != "{}" {
				em.send(ts.deltaEvent(), map[string]any{
					"item_id":      ts.itemID,
					"output_index": ts.outIndex,
					"delta":        ts.payloadText(call.Arguments),
				})
			}
		}
		em.send(ts.doneEvent(), map[string]any{
			"item_id":      ts.itemID,
			"output_index": ts.outIndex,
			ts.argField():  ts.payloadText(call.Arguments),
			"name":         call.Name,
		})
		em.send("response.output_item.done", map[string]any{
			"item_id":      ts.itemID,
			"output_index": ts.outIndex,
			"item": map[string]any{
				"id":          ts.itemID,
				"type":        ts.itemType(),
				"call_id":     call.ID,
				"name":        call.Name,
				ts.argField(): ts.payloadText(call.Arguments),
				"status":      "completed",
			},
		})
		ts.done = true
	}
}

// lookup finds the stream slot belonging to an assembled tool call.
func (st *streamState) lookup(call ToolCall) *toolStream {
	if ts, ok := st.tools[call.Index]; ok {
		return ts
	}
	for _, idx := range st.toolOrder {
		if ts := st.tools[idx]; ts != nil && ts.callID == call.ID {
			return ts
		}
	}
	return nil
}

func (st *streamState) toolFor(d CCToolCallDelta) *toolStream {
	idx := len(st.toolOrder)
	if d.Index != nil {
		idx = *d.Index
	}
	if ts, ok := st.tools[idx]; ok {
		if ts.name == "" {
			ts.name = d.Function.Name
		}
		if ts.callID == "" {
			ts.callID = d.ID
		}
		return ts
	}
	ts := &toolStream{itemID: newID(toolIDPrefix(d.Function.Name, st.custom)), name: d.Function.Name, callID: d.ID, custom: st.custom[d.Function.Name]}
	st.tools[idx] = ts
	st.toolOrder = append(st.toolOrder, idx)
	return ts
}

// toolIDPrefix picks the output-item id prefix. Codex rejects items whose id
// prefix does not match the item type, so downgraded custom tools get `ctc_`
// while ordinary function calls get `fc_`.
func toolIDPrefix(name string, custom map[string]bool) string {
	if custom[name] {
		return "ctc"
	}
	return "fc"
}

func idOf(s *itemState) string {
	if s == nil {
		return newID("msg")
	}
	return s.id
}

// failedResponse renders the terminal response.failed object: the skeleton
// with status flipped and the error slot filled. Only the classified code and
// a generic message go out — upstream details stay in the server log.
func failedResponse(id, model string, req *RRequest, code, message string) map[string]any {
	m := skeleton(id, model, req)
	m["status"] = "failed"
	m["error"] = map[string]any{
		"code":    code,
		"message": message,
	}
	return m
}

// skeleton renders the response object sent with response.created.
func skeleton(id, model string, req *RRequest) map[string]any {
	return map[string]any{
		"id":                  id,
		"object":              "response",
		"created_at":          time.Now().Unix(),
		"status":              "in_progress",
		"model":               model,
		"output":              []any{},
		"output_text":         "",
		"error":               nil,
		"incomplete_details":  nil,
		"parallel_tool_calls": req.ParallelToolCalls == nil || *req.ParallelToolCalls,
		"tool_choice":         "auto",
		"tools":               echoTools(req.Tools),
		"metadata":            req.Metadata,
	}
}
