package gemini

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"strings"
)

// ResponseEncoder encodes IR responses into Gemini generateContent format.
type ResponseEncoder struct{}

func (ResponseEncoder) EncodeResponse(resp *protocols.IRResponse) json.RawMessage {
	parts := buildGeminiParts(resp)

	finishReason := mapToGeminiFinishReason(resp.StopReason, len(resp.ToolCalls) > 0)

	geminiResp := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"role":  "model",
					"parts": parts,
				},
				"finishReason": finishReason,
			},
		},
		"usageMetadata": buildGeminiUsage(resp.Usage),
	}

	data, _ := json.Marshal(geminiResp)
	return data
}

// StreamEncoder encodes IR deltas into Gemini SSE format.
type StreamEncoder struct {
	usage      protocols.IRUsage
	model      string
	toolArgBuf map[int]string // index -> accumulated arguments
	toolNames  map[int]string
	stopReason string
	hasStop    bool
}

func NewStreamEncoder() *StreamEncoder {
	return &StreamEncoder{
		toolArgBuf: make(map[int]string),
		toolNames:  make(map[int]string),
	}
}

func (e *StreamEncoder) EncodeDeltas(deltas []protocols.IRStreamDelta) []protocols.SSEEvent {
	var events []protocols.SSEEvent

	for _, delta := range deltas {
		switch d := delta.(type) {
		case protocols.DeltaMessageStart:
			e.model = d.Model
		case protocols.DeltaText:
			events = append(events, geminiTextChunk(d.Text, e.model))
		case protocols.DeltaThinking:
			// The Gemini protocol marks thinking content with parts[].thought=true so
			// client SDKs can distinguish the reasoning summary from regular output.
			// Treating DeltaThinking as a plain DeltaText would surface the reasoning
			// process to the user as ordinary text, which is inconsistent with the
			// Codex / Claude codec paths.
			events = append(events, geminiThoughtChunk(d.Text, e.model))
		case protocols.DeltaToolCallStart:
			e.toolNames[d.Index] = d.Name
			e.toolArgBuf[d.Index] = ""
		case protocols.DeltaToolCallArgs:
			e.toolArgBuf[d.Index] += d.Arguments
			name, ok := e.toolNames[d.Index]
			if !ok {
				continue
			}
			// Only emit when we have complete JSON
			var argsMap map[string]interface{}
			if json.Unmarshal([]byte(e.toolArgBuf[d.Index]), &argsMap) == nil {
				events = append(events, geminiToolCallChunk(name, argsMap))
			}
		case protocols.DeltaUsage:
			// Field-level merge (via IRUsage.Merge): avoids a later completion-only
			// usage frame zeroing out PromptTokens / cache fields that were already
			// collected, when the upstream splits usage across several partial chunks.
			// Matches the claude / responses / chat encoder behavior.
			e.usage.Merge(d.Usage)
		case protocols.DeltaUnknown:
			var value json.RawMessage
			if json.Unmarshal(d.Raw, &value) == nil {
				events = append(events, protocols.SSEEvent{Data: string(value)})
			}
		case protocols.DeltaDone:
			e.stopReason = d.StopReason
			e.hasStop = true
		}
	}

	return events
}

func (e *StreamEncoder) EncodeDone() []protocols.SSEEvent {
	if !e.hasStop {
		return nil
	}
	finishReason := mapToGeminiFinishReason(e.stopReason, len(e.toolNames) > 0)
	usageMeta := buildGeminiUsage(e.usage)
	finishChunk := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content":      map[string]interface{}{"role": "model", "parts": []interface{}{}},
				"finishReason": finishReason,
			},
		},
		"usageMetadata": usageMeta,
	}
	data, _ := json.Marshal(finishChunk)
	return []protocols.SSEEvent{{Data: string(data)}}
}

func (e *StreamEncoder) Usage() protocols.IRUsage {
	return e.usage
}

// --- helpers ---

func buildGeminiParts(resp *protocols.IRResponse) []interface{} {
	var parts []interface{}

	// Thinking content
	if resp.ReasoningContent != "" {
		parts = append(parts, map[string]interface{}{
			"text":    resp.ReasoningContent,
			"thought": true,
		})
	}

	// Text content
	if resp.Content != "" {
		parts = append(parts, map[string]interface{}{"text": resp.Content})
	}

	// Tool calls
	for _, tc := range resp.ToolCalls {
		var args map[string]interface{}
		if json.Unmarshal([]byte(tc.Arguments), &args) != nil {
			args = map[string]interface{}{}
		}
		parts = append(parts, map[string]interface{}{
			"functionCall": map[string]interface{}{
				"name": tc.Name,
				"args": args,
			},
		})
	}

	if len(parts) == 0 {
		parts = append(parts, map[string]interface{}{"text": ""})
	}
	return parts
}

func buildGeminiUsage(u protocols.IRUsage) map[string]interface{} {
	meta := map[string]interface{}{
		"promptTokenCount":     u.PromptTokens,
		"candidatesTokenCount": u.CompletionTokens,
		"totalTokenCount":      u.TotalTokens,
	}
	if u.CacheReadTokens > 0 {
		meta["cachedContentTokenCount"] = u.CacheReadTokens
	}
	return meta
}

func geminiTextChunk(text, model string) protocols.SSEEvent {
	return geminiPartChunk(map[string]interface{}{"text": text}, model)
}

// geminiThoughtChunk emits a part with thought:true so client SDKs recognize
// it as reasoning content (rather than mixing it into the regular output shown
// to the user). Matches the official Gemini API behavior on thinking-capable models.
func geminiThoughtChunk(text, model string) protocols.SSEEvent {
	return geminiPartChunk(map[string]interface{}{"text": text, "thought": true}, model)
}

func geminiPartChunk(part map[string]interface{}, model string) protocols.SSEEvent {
	chunk := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"role":  "model",
					"parts": []interface{}{part},
				},
			},
		},
	}
	if model != "" {
		chunk["modelVersion"] = model
	}
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

func geminiToolCallChunk(name string, args map[string]interface{}) protocols.SSEEvent {
	chunk := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"role": "model",
					"parts": []interface{}{
						map[string]interface{}{
							"functionCall": map[string]interface{}{
								"name": name,
								"args": args,
							},
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

func mapToGeminiFinishReason(reason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "STOP"
	}
	switch reason {
	case "stop", "tool_calls":
		return "STOP"
	case "length", "max_tokens":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	case "error":
		// Explicit upstream stream failure (ResponsesStreamDecoder already emits
		// protocols.DeltaDone{stop_reason="error"}): the Gemini wire protocol has no
		// standard "error" finishReason, but OTHER conveys an abnormal termination
		// far more honestly than misreporting STOP, which would let the client
		// believe it received a complete response.
		return "OTHER"
	default:
		return "STOP"
	}
}

// StreamDecoder decodes Gemini SSE/JSON Lines into IR deltas.
// Used when reading from a Gemini upstream.
type StreamDecoder struct {
	buffer      string
	first       bool
	toolCallIdx int
}

func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{first: true}
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
			payload := strings.TrimSpace(line)
			payload, ok := strings.CutPrefix(payload, "data: ")
			if !ok {
				continue
			}
			payload = strings.TrimSpace(payload)
			if payload == "" {
				continue
			}
			deltas = append(deltas, d.parseGeminiChunk(json.RawMessage(payload))...)
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

func (d *StreamDecoder) parseGeminiChunk(raw json.RawMessage) []protocols.IRStreamDelta {
	var chunk struct {
		UsageMetadata *struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			TotalTokenCount         int `json:"totalTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
		Candidates   []struct {
			Content *struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}

	if json.Unmarshal(raw, &chunk) != nil {
		return nil
	}

	var deltas []protocols.IRStreamDelta

	if d.first {
		d.first = false
		deltas = append(deltas, protocols.DeltaMessageStart{
			ID:    "gen-" + protocols.RandomString(16),
			Model: chunk.ModelVersion,
		})
	}

	for _, cand := range chunk.Candidates {
		if cand.Content != nil {
			for _, partRaw := range cand.Content.Parts {
				var part map[string]json.RawMessage
				if json.Unmarshal(partRaw, &part) != nil {
					continue
				}

				if textRaw, ok := part["text"]; ok {
					var text string
					_ = json.Unmarshal(textRaw, &text)
					if thoughtRaw, ok := part["thought"]; ok {
						var thought bool
						if json.Unmarshal(thoughtRaw, &thought) == nil && thought {
							deltas = append(deltas, protocols.DeltaThinking{Text: text})
							continue
						}
					}
					if text != "" {
						deltas = append(deltas, protocols.DeltaText{Text: text})
					}
				}

				if fcRaw, ok := part["functionCall"]; ok {
					var fc struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					}
					if json.Unmarshal(fcRaw, &fc) == nil {
						id := "call_" + protocols.RandomString(12)
						deltas = append(deltas, protocols.DeltaToolCallStart{
							Index: d.toolCallIdx, ID: id, Name: fc.Name,
						})
						d.toolCallIdx++
						if string(fc.Args) != "" && string(fc.Args) != "{}" {
							deltas = append(deltas, protocols.DeltaToolCallArgs{
								Index: d.toolCallIdx - 1, Arguments: string(fc.Args),
							})
						}
					}
				}
			}
		}

		if cand.FinishReason != "" {
			var reason string
			switch cand.FinishReason {
			case "MAX_TOKENS":
				reason = "length"
			case "STOP":
				reason = "stop"
			default:
				reason = strings.ToLower(cand.FinishReason)
			}
			deltas = append(deltas, protocols.DeltaDone{StopReason: reason})
		}
	}

	if chunk.UsageMetadata != nil {
		deltas = append(deltas, protocols.DeltaUsage{Usage: protocols.IRUsage{
			PromptTokens:          chunk.UsageMetadata.PromptTokenCount,
			CompletionTokens:      chunk.UsageMetadata.CandidatesTokenCount,
			TotalTokens:           chunk.UsageMetadata.TotalTokenCount,
			CacheReadTokens:       chunk.UsageMetadata.CachedContentTokenCount,
			CacheIncludedInPrompt: true,
		}})
	}

	return deltas
}

// ResponseDecoder decodes a Gemini generateContent JSON response into IR.
type ResponseDecoder struct{}

func (ResponseDecoder) DecodeResponse(body json.RawMessage) (*protocols.IRResponse, error) {
	var resp struct {
		Candidates []struct {
			Content *struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			TotalTokenCount         int `json:"totalTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	irResp := &protocols.IRResponse{
		ID:    "chatcmpl-" + protocols.RandomString(24),
		Model: resp.ModelVersion,
		Usage: protocols.IRUsage{CacheIncludedInPrompt: true},
	}

	for _, cand := range resp.Candidates {
		irResp.StopReason = mapFromGeminiFinishReason(cand.FinishReason)
		if cand.Content == nil {
			continue
		}
		for _, partRaw := range cand.Content.Parts {
			var part map[string]json.RawMessage
			if json.Unmarshal(partRaw, &part) != nil {
				continue
			}

			if textRaw, ok := part["text"]; ok {
				var text string
				_ = json.Unmarshal(textRaw, &text)
				if thoughtRaw, ok := part["thought"]; ok {
					var thought bool
					if json.Unmarshal(thoughtRaw, &thought) == nil && thought {
						irResp.ReasoningContent += text
						continue
					}
				}
				irResp.Content += text
			}

			if fcRaw, ok := part["functionCall"]; ok {
				var fc struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				}
				if json.Unmarshal(fcRaw, &fc) == nil {
					args := string(fc.Args)
					if args == "" {
						args = "{}"
					}
					irResp.ToolCalls = append(irResp.ToolCalls, protocols.IRToolCall{
						ID:        "call_" + protocols.RandomString(12),
						Name:      fc.Name,
						Arguments: args,
					})
				}
			}
		}
	}

	if resp.UsageMetadata != nil {
		irResp.Usage = protocols.IRUsage{
			PromptTokens:          resp.UsageMetadata.PromptTokenCount,
			CompletionTokens:      resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:           resp.UsageMetadata.TotalTokenCount,
			CacheReadTokens:       resp.UsageMetadata.CachedContentTokenCount,
			CacheIncludedInPrompt: true,
		}
	}

	return irResp, nil
}

func mapFromGeminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	default:
		return strings.ToLower(reason)
	}
}
