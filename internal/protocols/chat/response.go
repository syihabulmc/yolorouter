package chat

import (
	"encoding/json"
	"fmt"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"strings"
)

// ResponseDecoder decodes OpenAI Chat Completions responses into IR.
type ResponseDecoder struct{}

func (ResponseDecoder) DecodeResponse(body json.RawMessage) (*protocols.IRResponse, error) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role             string           `json:"role"`
				Content          string           `json:"content"`
				ReasoningContent string           `json:"reasoning_content,omitempty"`
				ToolCalls        []map[string]any `json:"tool_calls,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details,omitempty"`
			// DeepSeek splits prompt_tokens into hit + miss. Only the hit half is
			// read: it is the cache-read count. The miss half is the non-cached
			// remainder, which netPromptTokens already derives as
			// prompt_tokens - cache_read; it is NOT a cache write (DeepSeek's
			// cache is implicit and has no separate write line).
			PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"`
		} `json:"usage,omitempty"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse openai response: %w", err)
	}

	irResp := protocols.NewIRResponse(resp.ID, resp.Model)

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		irResp.Content = choice.Message.Content
		irResp.ReasoningContent = choice.Message.ReasoningContent
		irResp.StopReason = choice.FinishReason

		for _, tc := range choice.Message.ToolCalls {
			fn, _ := tc["function"].(map[string]interface{})
			if fn == nil {
				continue
			}
			id, _ := tc["id"].(string)
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			irResp.ToolCalls = append(irResp.ToolCalls, protocols.IRToolCall{
				ID: id, Name: name, Arguments: args,
			})
		}
	}

	if resp.Usage != nil {
		irResp.Usage = protocols.IRUsage{
			PromptTokens:          resp.Usage.PromptTokens,
			CompletionTokens:      resp.Usage.CompletionTokens,
			TotalTokens:           resp.Usage.TotalTokens,
			CacheIncludedInPrompt: true,
		}
		if resp.Usage.PromptTokensDetails != nil && resp.Usage.PromptTokensDetails.CachedTokens > 0 {
			irResp.Usage.CacheReadTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		if resp.Usage.PromptCacheHitTokens > 0 {
			// DeepSeek uses prompt_cache_hit_tokens instead of prompt_tokens_details.cached_tokens
			irResp.Usage.CacheReadTokens = resp.Usage.PromptCacheHitTokens
		}
	}

	return irResp, nil
}

// StreamDecoder decodes OpenAI SSE stream chunks into IR deltas.
type StreamDecoder struct {
	buffer     string
	started    bool
	done       bool
	toolArgBuf map[int]string // index -> accumulated arguments
}

func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{
		toolArgBuf: make(map[int]string),
	}
}

func (d *StreamDecoder) DecodeChunk(raw string) ([]protocols.IRStreamDelta, error) {
	d.buffer += raw
	var deltas []protocols.IRStreamDelta

	for {
		pos := strings.Index(d.buffer, "\n\n")
		if pos < 0 {
			break
		}
		block := d.buffer[:pos]
		d.buffer = d.buffer[pos+2:]

		for _, line := range strings.Split(block, "\n") {
			data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
			if !ok {
				continue
			}
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				if !d.done {
					d.done = true
					deltas = append(deltas, protocols.DeltaDone{StopReason: "stop"})
				}
				continue
			}
			chunk := json.RawMessage(data)
			deltas = append(deltas, d.parseChunk(chunk)...)
		}
	}

	return deltas, nil
}

func (d *StreamDecoder) Finish() ([]protocols.IRStreamDelta, error) {
	if strings.TrimSpace(d.buffer) == "" {
		return nil, nil
	}
	remaining := d.buffer
	d.buffer = ""
	return d.DecodeChunk(remaining + "\n\n")
}

func (d *StreamDecoder) parseChunk(raw json.RawMessage) []protocols.IRStreamDelta {
	var chunk struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Content          string           `json:"content"`
				ReasoningContent string           `json:"reasoning_content"`
				ToolCalls        []map[string]any `json:"tool_calls,omitempty"`
				Role             string           `json:"role"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details,omitempty"`
			// DeepSeek splits prompt_tokens into hit + miss. Only the hit half is
			// read: it is the cache-read count. The miss half is the non-cached
			// remainder, which netPromptTokens already derives as
			// prompt_tokens - cache_read; it is NOT a cache write (DeepSeek's
			// cache is implicit and has no separate write line).
			PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"`
		} `json:"usage,omitempty"`
	}

	if json.Unmarshal(raw, &chunk) != nil {
		return nil
	}

	var deltas []protocols.IRStreamDelta

	// Message start
	if !d.started && chunk.ID != "" {
		d.started = true
		deltas = append(deltas, protocols.DeltaMessageStart{ID: chunk.ID, Model: chunk.Model})
	}

	// Content
	if len(chunk.Choices) > 0 {
		choice := chunk.Choices[0]

		if choice.Delta.ReasoningContent != "" {
			deltas = append(deltas, protocols.DeltaThinking{Text: choice.Delta.ReasoningContent})
		}
		if choice.Delta.Content != "" {
			deltas = append(deltas, protocols.DeltaText{Text: choice.Delta.Content})
		}

		// Tool calls with argument accumulation
		for _, tc := range choice.Delta.ToolCalls {
			fn, _ := tc["function"].(map[string]interface{})
			if fn == nil {
				continue
			}
			idx := 0
			if v, ok := tc["index"].(float64); ok {
				idx = int(v)
			}
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			id, _ := tc["id"].(string)

			if name != "" || id != "" {
				if id == "" {
					id = "call_" + protocols.RandomString(12)
				}
				d.toolArgBuf[idx] = ""
				deltas = append(deltas, protocols.DeltaToolCallStart{
					Index: idx, ID: id, Name: name,
				})
			}
			if args != "" {
				d.toolArgBuf[idx] += args
				deltas = append(deltas, protocols.DeltaToolCallArgs{
					Index: idx, Arguments: args,
				})
			}
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" && !d.done {
			d.done = true
			deltas = append(deltas, protocols.DeltaDone{StopReason: *choice.FinishReason})
		}
	}

	// Usage
	if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
		usage := protocols.IRUsage{
			PromptTokens:          chunk.Usage.PromptTokens,
			CompletionTokens:      chunk.Usage.CompletionTokens,
			TotalTokens:           chunk.Usage.TotalTokens,
			CacheIncludedInPrompt: true,
		}
		if chunk.Usage.PromptTokensDetails != nil {
			usage.CacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
		if chunk.Usage.PromptCacheHitTokens > 0 {
			// DeepSeek uses prompt_cache_hit_tokens instead of prompt_tokens_details.cached_tokens
			usage.CacheReadTokens = chunk.Usage.PromptCacheHitTokens
		}
		deltas = append(deltas, protocols.DeltaUsage{Usage: usage})
	}

	return deltas
}
