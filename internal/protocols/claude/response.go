package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// ResponseDecoder decodes a Claude Messages API JSON response into IR.
type ResponseDecoder struct{}

func (ResponseDecoder) DecodeResponse(body json.RawMessage) (*protocols.IRResponse, error) {
	var resp struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text,omitempty"`
			ID       string          `json:"id,omitempty"`
			Name     string          `json:"name,omitempty"`
			Input    json.RawMessage `json:"input,omitempty"`
			Thinking string          `json:"thinking,omitempty"`
		} `json:"content"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	irResp := &protocols.IRResponse{
		ID:    resp.ID,
		Model: resp.Model,
		Usage: protocols.IRUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens,
			CacheWriteTokens: resp.Usage.CacheCreationInputTokens,
			CacheReadTokens:  resp.Usage.CacheReadInputTokens,
		},
	}

	irResp.StopReason = claudeMapStopReason(resp.StopReason)

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			irResp.Content += block.Text
		case "tool_use":
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			irResp.ToolCalls = append(irResp.ToolCalls, protocols.IRToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		case "thinking":
			irResp.ReasoningContent += block.Thinking
		}
	}

	return irResp, nil
}

// StreamDecoder decodes Claude SSE events into IR deltas.
type StreamDecoder struct {
	usage       protocols.IRUsage
	id          string
	model       string
	started     bool
	completed   bool   // the stream is only considered to have ended naturally after message_stop
	doneEmitted bool   // whether DeltaDone was already emitted during message_delta (the standard upstream path)
	upstreamErr error  // an explicit upstream type:"error" event; makes Finish return an error so the caller doesn't settle billing incorrectly
	blockType   string // current content_block type
	blockIndex  int
	toolNames   map[int]string
	toolCallIdx int
	toolIdxMap  map[int]int // content block index -> tool call index
}

func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{
		toolNames:  make(map[int]string),
		toolIdxMap: make(map[int]int),
	}
}

func (d *StreamDecoder) DecodeChunk(raw string) ([]protocols.IRStreamDelta, error) {
	line := strings.TrimSpace(raw)

	if line == "" || strings.HasPrefix(line, "event:") {
		return nil, nil
	}

	payload, ok := strings.CutPrefix(line, "data: ")
	if !ok {
		return nil, nil
	}
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil, nil
	}

	var event struct {
		Type    string `json:"type"`
		Index   int    `json:"index,omitempty"`
		Message *struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
			} `json:"usage"`
		} `json:"message,omitempty"`
		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text,omitempty"`
			PartialJSON string `json:"partial_json,omitempty"`
			StopReason  string `json:"stop_reason,omitempty"`
			Thinking    string `json:"thinking,omitempty"`
		} `json:"delta,omitempty"`
		ContentBlock *struct {
			Type  string          `json:"type"`
			ID    string          `json:"id,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content_block,omitempty"`
		Usage *struct {
			OutputTokens             int `json:"output_tokens"`
			InputTokens              int `json:"input_tokens,omitempty"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
		} `json:"usage,omitempty"`
	}
	if json.Unmarshal([]byte(payload), &event) != nil {
		return nil, nil
	}

	var deltas []protocols.IRStreamDelta

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			d.id = event.Message.ID
			d.model = event.Message.Model
			d.started = true
			deltas = append(deltas, protocols.DeltaMessageStart{
				ID:    d.id,
				Model: d.model,
			})
			if event.Message.Usage != nil {
				d.usage = protocols.IRUsage{
					PromptTokens:     event.Message.Usage.InputTokens,
					CompletionTokens: 0,
					TotalTokens:      event.Message.Usage.InputTokens + event.Message.Usage.CacheCreationInputTokens + event.Message.Usage.CacheReadInputTokens,
					CacheWriteTokens: event.Message.Usage.CacheCreationInputTokens,
					CacheReadTokens:  event.Message.Usage.CacheReadInputTokens,
				}
			}
		}

	case "content_block_start":
		if event.ContentBlock != nil {
			d.blockType = event.ContentBlock.Type
			d.blockIndex = event.Index
			if event.ContentBlock.Type == "tool_use" {
				d.toolNames[event.Index] = event.ContentBlock.Name
				d.toolIdxMap[event.Index] = d.toolCallIdx
				deltas = append(deltas, protocols.DeltaToolCallStart{
					Index: d.toolCallIdx,
					ID:    event.ContentBlock.ID,
					Name:  event.ContentBlock.Name,
				})
				d.toolCallIdx++
			}
		}

	case "content_block_delta":
		if event.Delta == nil {
			break
		}
		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				deltas = append(deltas, protocols.DeltaText{Text: event.Delta.Text})
			}
		case "thinking_delta":
			if event.Delta.Thinking != "" {
				deltas = append(deltas, protocols.DeltaThinking{Text: event.Delta.Thinking})
			}
		case "input_json_delta":
			name := d.toolNames[event.Index]
			tcIdx := d.toolIdxMap[event.Index]
			if name != "" && event.Delta.PartialJSON != "" {
				deltas = append(deltas, protocols.DeltaToolCallArgs{
					Index:     tcIdx,
					Arguments: event.Delta.PartialJSON,
				})
			}
		}

	case "content_block_stop":
		// nothing to do

	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != "" {
			reason := claudeMapStopReason(event.Delta.StopReason)
			deltas = append(deltas, protocols.DeltaDone{StopReason: reason})
			d.doneEmitted = true
		}
		if event.Usage != nil {
			d.usage.CompletionTokens = event.Usage.OutputTokens
			// message_delta is the terminal usage event: its non-zero fields
			// replace message_start's value (last-wins). A converting upstream
			// (OpenAI/Gemini/Responses, whose usage only resolves at stream end)
			// emits message_start with input_tokens=0 and reports the real input
			// here, so adopt the delta's value whenever it is non-zero.
			if event.Usage.CacheCreationInputTokens > 0 {
				d.usage.CacheWriteTokens = event.Usage.CacheCreationInputTokens
			}
			if event.Usage.CacheReadInputTokens > 0 {
				d.usage.CacheReadTokens = event.Usage.CacheReadInputTokens
			}
			if event.Usage.InputTokens > 0 {
				d.usage.PromptTokens = event.Usage.InputTokens
			}
			// TotalTokens is recomputed from the per-field totals on every
			// message_delta so it stays consistent when a last-wins update
			// lowers a field (e.g. message_delta correcting message_start's
			// input downward). A "never decrease" high-water mark here would
			// leave TotalTokens above the sum of its parts and leak into billing.
			d.usage.TotalTokens = d.usage.PromptTokens + event.Usage.OutputTokens + d.usage.CacheWriteTokens + d.usage.CacheReadTokens
			// Extract Anthropic web search count from raw payload
			var wsExtract struct {
				Usage *struct {
					ServerToolUse *struct {
						WebSearchRequests int `json:"web_search_requests"`
					} `json:"server_tool_use"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(payload), &wsExtract) == nil && wsExtract.Usage != nil && wsExtract.Usage.ServerToolUse != nil {
				d.usage.WebSearchCount = wsExtract.Usage.ServerToolUse.WebSearchRequests
			}
			deltas = append(deltas, protocols.DeltaUsage{Usage: d.usage})
		}

	case "message_stop":
		// terminal event: marks the stream as having ended naturally, so Finish can
		// fall back to emitting the usage captured from message_start
		d.completed = true

	case "ping":
		// ignore

	case "error":
		// An explicit upstream error event (e.g.
		// {"type":"error","error":{"type":"overloaded_error","message":"..."}}).
		// Finish must return an error here so the caller takes the failure path
		// (502, user not billed) instead of treating a subsequent EOF as a
		// successful settlement and silently swallowing the in-stream error.
		// If a stream emits multiple error events, only the first one is kept.
		if d.upstreamErr == nil {
			var errPayload struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if jerr := json.Unmarshal([]byte(payload), &errPayload); jerr == nil && errPayload.Error.Message != "" {
				d.upstreamErr = fmt.Errorf("upstream claude error event: %s: %s", errPayload.Error.Type, errPayload.Error.Message)
			} else {
				d.upstreamErr = errors.New("upstream claude error event")
			}
		}
	}

	return deltas, nil
}

func (d *StreamDecoder) Finish() ([]protocols.IRStreamDelta, error) {
	// An explicit upstream error event takes priority: the caller takes the failure
	// path, and partial usage is still passed through to record provider cost, but
	// DeltaDone is not emitted so the relay helper doesn't treat the stream as
	// successful (IRStreamRelay checks finishErr and skips EncodeDone; sawDone must
	// not be set either).
	if d.upstreamErr != nil {
		var out []protocols.IRStreamDelta
		if d.usage.PromptTokens > 0 || d.usage.CompletionTokens > 0 || d.usage.CacheWriteTokens > 0 || d.usage.CacheReadTokens > 0 {
			out = append(out, protocols.DeltaUsage{Usage: d.usage})
		}
		return out, d.upstreamErr
	}
	// Only fall back to emitting a terminal signal after message_stop has been
	// received (d.completed=true) -- this prevents a truncated stream from being
	// billed as a successful completion.
	//
	// Two upstream paths need to be covered:
	// 1) Standard Claude: message_delta already emitted DeltaDone + full usage, so
	//    this only needs to add one more DeltaUsage (last-wins, doesn't affect
	//    encoder.Usage); DeltaDone is not emitted again.
	// 2) Some Anthropic-compatible upstream providers jump straight from
	//    content_block_stop to message_stop without a message_delta event, so this
	//    must emit DeltaDone (so the relay helper's sawDone flag recognizes a clean
	//    completion) plus DeltaUsage (passing message_start's input_tokens through
	//    to the billing layer).
	if !d.completed || !d.started {
		return nil, nil
	}
	var out []protocols.IRStreamDelta
	if !d.doneEmitted {
		out = append(out, protocols.DeltaDone{StopReason: "stop"})
		d.doneEmitted = true
	}
	if d.usage.PromptTokens > 0 || d.usage.CompletionTokens > 0 || d.usage.CacheWriteTokens > 0 || d.usage.CacheReadTokens > 0 {
		out = append(out, protocols.DeltaUsage{Usage: d.usage})
	}
	return out, nil
}

// claudeMapStopReason maps Claude API stop_reason to IR stop_reason.
func claudeMapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		if reason != "" {
			return strings.ToLower(reason)
		}
		return "stop"
	}
}

// irToClaudeStopReason maps IR stop_reason to Claude API stop_reason.
func irToClaudeStopReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return reason
	}
}
