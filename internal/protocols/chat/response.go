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
				// OpenRouter documents a cache-WRITE count nested here, beside
				// cached_tokens: "Number of tokens written to the cache. This
				// appears on the first request when establishing a new cache
				// entry." It is the standard spelling and takes precedence over
				// the top-level alias below.
				//
				// A pointer so "field absent" is distinguishable from "field
				// present and 0". An explicit zero asserts there was no cache
				// write and must win over a stale non-zero alias — an int would
				// read it as absent and bill the alias instead.
				CacheWriteTokens *int `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details,omitempty"`
			// DeepSeek splits prompt_tokens into hit + miss. Only the hit half is
			// read: it is the cache-read count. The miss half is the non-cached
			// remainder, which netPromptTokens already derives as
			// prompt_tokens - cache_read; it is NOT a cache write (DeepSeek's
			// cache is implicit and has no separate write line).
			PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"`
			// Non-standard: OpenAI has no cache-write field. Gateways fronting
			// an Anthropic model (this one included, see openAIWireUsage) carry
			// the count under Anthropic's own name so it survives the hop.
			// Like cached_tokens it is a breakdown OF prompt_tokens, so it is
			// recorded, never added.
			CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
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
		// Cache WRITE has two spellings in the wild and they name the same
		// breakdown of prompt_tokens, so exactly one is taken — never summed.
		// OpenRouter's nested cache_write_tokens is the documented contract and
		// wins; the top-level alias is what new-api-style gateways (including
		// this one's own Gemini egress) emit and serves as the fallback.
		//
		// Precedence is by field PRESENCE, not by value: an explicitly reported
		// 0 asserts "no cache write" and must beat a stale non-zero alias. A
		// negative value likewise survives rather than being masked, so the
		// gateway's coherence check can refuse the whole record.
		irResp.Usage.CacheWriteTokens = resp.Usage.CacheCreationInputTokens
		if resp.Usage.PromptTokensDetails != nil && resp.Usage.PromptTokensDetails.CacheWriteTokens != nil {
			irResp.Usage.CacheWriteTokens = *resp.Usage.PromptTokensDetails.CacheWriteTokens
		}
		// Set the verdict at the IR exit so every consumer reads Invalid instead
		// of re-judging on data the conversion has since distorted. See
		// IRUsage.IsIncoherent.
		irResp.Usage.Invalid = irResp.Usage.IsIncoherent()
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
				// OpenRouter documents a cache-WRITE count nested here, beside
				// cached_tokens: "Number of tokens written to the cache. This
				// appears on the first request when establishing a new cache
				// entry." It is the standard spelling and takes precedence over
				// the top-level alias below.
				//
				// A pointer so "field absent" is distinguishable from "field
				// present and 0". An explicit zero asserts there was no cache
				// write and must win over a stale non-zero alias — an int would
				// read it as absent and bill the alias instead.
				CacheWriteTokens *int `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details,omitempty"`
			// DeepSeek splits prompt_tokens into hit + miss. Only the hit half is
			// read: it is the cache-read count. The miss half is the non-cached
			// remainder, which netPromptTokens already derives as
			// prompt_tokens - cache_read; it is NOT a cache write (DeepSeek's
			// cache is implicit and has no separate write line).
			PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"`
			// Non-standard: OpenAI has no cache-write field. Gateways fronting
			// an Anthropic model (this one included, see openAIWireUsage) carry
			// the count under Anthropic's own name so it survives the hop.
			// Like cached_tokens it is a breakdown OF prompt_tokens, so it is
			// recorded, never added.
			CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
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
	// Gated on presence alone. Keying the gate on prompt/completion being
	// non-zero dropped a usage chunk that carried ONLY cache counts — a shape
	// some upstreams send when they split usage across frames — so the cache
	// write stayed at whatever an earlier frame had merged and its tokens were
	// billed as fresh input. Whether the assembled record is worth emitting is
	// decided below, after every field has been read.
	if chunk.Usage != nil {
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
		// Nested cache_write_tokens wins over the top-level alias; exactly one
		// is taken, never summed. See the non-streaming decoder above.
		usage.CacheWriteTokens = chunk.Usage.CacheCreationInputTokens
		if chunk.Usage.PromptTokensDetails != nil && chunk.Usage.PromptTokensDetails.CacheWriteTokens != nil {
			usage.CacheWriteTokens = *chunk.Usage.PromptTokensDetails.CacheWriteTokens
		}
		// An impossible record cannot be carried through IRUsage.Merge, which
		// copies only values greater than zero, so the negative or oversized cache
		// that proved it wrong would silently vanish and the remaining counts be
		// billed as sound. The frame is MARKED rather than dropped: dropping it
		// would leave whatever an earlier frame merged in place, and a
		// finish_reason in this same chunk would still complete the stream and
		// bill those stale counts. Merge propagates Invalid one-way, so the
		// verdict survives to every consumer.
		usage.Invalid = usage.IsIncoherent()
		// Cache counts are enough on their own to make a frame worth emitting;
		// requiring prompt/completion is what dropped cache-only chunks.
		if usage.Invalid ||
			usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 ||
			usage.CacheReadTokens != 0 || usage.CacheWriteTokens != 0 {
			deltas = append(deltas, protocols.DeltaUsage{Usage: usage})
		}
	}

	return deltas
}
