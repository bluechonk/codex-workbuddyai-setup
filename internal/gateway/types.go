package gateway

import "encoding/json"

// ---------------------------------------------------------------------------
// OpenAI Responses API (inbound from Codex)
// ---------------------------------------------------------------------------

// RItem is one element of the `input` array.
type RItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	Status    string          `json:"status"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	Output    json.RawMessage `json:"output"`
}

// RTool is one element of the `tools` array.
type RTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Format      json.RawMessage `json:"format"`
	Strict      *bool           `json:"strict"`
}

// RRequest is an inbound POST /v1/responses body.
type RRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       json.RawMessage `json:"instructions"`
	Tools              []RTool         `json:"tools"`
	ToolChoice         json.RawMessage `json:"tool_choice"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls"`
	Reasoning          json.RawMessage `json:"reasoning"`
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"top_p"`
	MaxOutputTokens    *int            `json:"max_output_tokens"`
	Stream             bool            `json:"stream"`
	Store              *bool           `json:"store"`
	PreviousResponseID string          `json:"previous_response_id"`
	Text               json.RawMessage `json:"text"`
	Metadata           json.RawMessage `json:"metadata"`
	Truncation         string          `json:"truncation"`
}

// ---------------------------------------------------------------------------
// OpenAI Chat Completions API (outbound to WorkBuddyAI)
// ---------------------------------------------------------------------------

// CCRequest is the upstream chat-completions body.
type CCRequest struct {
	Model            string           `json:"model"`
	Messages         []CCMessage      `json:"messages"`
	Stream           bool             `json:"stream"`
	StreamOptions    *CCStreamOptions `json:"stream_options,omitempty"`
	Temperature      *float64         `json:"temperature,omitempty"`
	TopP             *float64         `json:"top_p,omitempty"`
	MaxTokens        *int             `json:"max_tokens,omitempty"`
	Tools            []CCTool         `json:"tools,omitempty"`
	ToolChoice       any              `json:"tool_choice,omitempty"`
	ParallelToolCall *bool            `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort  string           `json:"reasoning_effort,omitempty"`
	ResponseFormat   any              `json:"response_format,omitempty"`
}

// CCStreamOptions asks the upstream for a final usage chunk.
type CCStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// CCMessage is one chat message. Content is either a string or an array of
// content parts.
type CCMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content"`
	ToolCalls  []CCToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

// CCToolCall is an assistant-originated function invocation.
type CCToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function CCToolCallFunc `json:"function"`
	Index    *int           `json:"-"`
}

// CCToolCallFunc carries the tool name and its JSON arguments.
type CCToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// CCTool declares a function the model may call.
type CCTool struct {
	Type     string       `json:"type"`
	Function CCToolFnSpec `json:"function"`
}

// CCToolFnSpec is the function description inside a tool declaration.
type CCToolFnSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ---------------------------------------------------------------------------
// Upstream streaming chunks
// ---------------------------------------------------------------------------

// Chunk is one SSE payload emitted by the upstream chat endpoint.
type Chunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string            `json:"role"`
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []CCToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// CCToolCallDelta is an incremental tool-call fragment.
type CCToolCallDelta struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	Index *int `json:"index"`
}

// Usage is the token accounting reported by the upstream.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CompletionDetail struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptDetail struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// ---------------------------------------------------------------------------
// Responses API objects (outbound to Codex)
// ---------------------------------------------------------------------------

// RUsage is the Responses usage object.
type RUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	InputDetail  struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputDetail struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// RResponse is the Responses object returned to Codex.
type RResponse struct {
	ID                 string          `json:"id"`
	Object             string          `json:"object"`
	CreatedAt          int64           `json:"created_at"`
	Status             string          `json:"status"`
	Model              string          `json:"model"`
	Output             []any           `json:"output"`
	OutputText         string          `json:"output_text"`
	Usage              *RUsage         `json:"usage,omitempty"`
	Error              any             `json:"error"`
	IncompleteDetails  any             `json:"incomplete_details"`
	Instructions       any             `json:"instructions"`
	ParallelToolCalls  bool            `json:"parallel_tool_calls"`
	ToolChoice         any             `json:"tool_choice"`
	Tools              []any           `json:"tools"`
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"top_p"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	Reasoning          json.RawMessage `json:"reasoning,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	Truncation         string          `json:"truncation,omitempty"`
	Text               json.RawMessage `json:"text,omitempty"`
	PreviousResponseID *string         `json:"previous_response_id"`
}
