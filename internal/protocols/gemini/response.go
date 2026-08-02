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

// buildGeminiUsage renders IR usage as Gemini's usageMetadata.
//
// Emits GROSS counts: Gemini documents promptTokenCount as the total effective
// prompt size, with cachedContentTokenCount a breakdown of it. Forwarding the
// raw IR PromptTokens would be wrong for an Anthropic upstream, whose count
// excludes cache — the cached portion would silently disappear from the input.
func buildGeminiUsage(u protocols.IRUsage) map[string]interface{} {
	// A record the gateway itself refused publishes nothing: emitting sanitized
	// counts would hand the client — and any downstream gateway billing from
	// them — numbers we already decided were impossible. null is the wire's
	// existing word for "unknown", and unknown is not zero.
	// HasNegativeCount as well as the flag: the non-streaming decoders keep a
	// bad count without marking it, and IRNonStreamRelay encodes the response
	// BEFORE the billing gate runs — so without this the client would receive
	// sanitized-looking usage for a record the gateway then refuses to bill.
	if u.Invalid || protocols.HasNegativeCount(u) {
		return nil
	}
	meta := map[string]interface{}{
		"promptTokenCount":     u.GrossPromptTokens(),
		"candidatesTokenCount": u.CompletionTokens,
		"totalTokenCount":      u.GrossTotalTokens(),
	}
	if u.CacheReadTokens > 0 {
		meta["cachedContentTokenCount"] = u.CacheReadTokens
	}
	// Non-standard cache-write breakdown; see protocols.CacheWriteAliasField.
	// Deliberately snake_case among Gemini's camelCase fields — it is not a
	// Google field and should not look like one.
	if u.CacheWriteTokens > 0 {
		meta[protocols.CacheWriteAliasField] = u.CacheWriteTokens
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

// usageMetadata is the token-accounting block both the streaming and the
// non-streaming decoder read. Shared so the two paths can never drift into
// reporting different totals for the same response.
type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
	// Thinking-model reasoning tokens. Whether candidatesTokenCount already
	// contains them depends on which backend answered — the Google AI endpoint
	// folds them in, Vertex AI reports them alongside — so this field can never
	// be added to candidatesTokenCount unconditionally. See toIRUsage.
	ThoughtsTokenCount int `json:"thoughtsTokenCount,omitempty"`
	// Tokens from tool-execution results fed back to the model. Google
	// documents these as input, and totalTokenCount counts them, so they bill
	// at the input rate and must be excluded when deriving output from the
	// total.
	ToolUsePromptTokenCount int `json:"toolUsePromptTokenCount,omitempty"`
	// Non-standard cache-write breakdown; see protocols.CacheWriteAliasField.
	// Gemini has no cache-write concept of its own, so this only ever appears
	// when the upstream is another gateway fronting an Anthropic model. Like
	// cachedContentTokenCount it sits INSIDE promptTokenCount.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CachedContentTokenCount  int `json:"cachedContentTokenCount,omitempty"`
}

// toIRUsage converts the block, reporting false when any count is negative.
// The negative check has to happen HERE rather than downstream: folding
// thoughts into candidates sums two numbers, and a sum hides the sign of its
// parts (10 + -5 looks like a perfectly ordinary 5). Rejecting the whole block
// leaves usage unknown, which bills nothing — strictly safer than billing a
// plausible-looking number derived from garbage.
func (m *usageMetadata) toIRUsage() (protocols.IRUsage, bool) {
	if m.PromptTokenCount < 0 || m.CandidatesTokenCount < 0 || m.ThoughtsTokenCount < 0 ||
		m.TotalTokenCount < 0 || m.CachedContentTokenCount < 0 || m.ToolUsePromptTokenCount < 0 ||
		m.CacheCreationInputTokens < 0 {
		return protocols.IRUsage{}, false
	}
	prompt := m.promptTokens()
	completion := m.completionTokens()
	// In the normal case completion was derived from the total, so this holds
	// already. It only bites when the total was unusable and completion fell
	// back to the sum: reporting the stated total then would hand downstream a
	// triple whose parts exceed their own total, which the gateway's coherence
	// check reads as garbage and refuses to bill at all.
	total := max(m.TotalTokenCount, prompt+completion)
	return protocols.IRUsage{
		PromptTokens:          prompt,
		CompletionTokens:      completion,
		ReasoningTokens:       m.ThoughtsTokenCount,
		TotalTokens:           total,
		CacheReadTokens:       m.CachedContentTokenCount,
		CacheWriteTokens:      m.CacheCreationInputTokens,
		CacheIncludedInPrompt: true,
	}, true
}

// promptTokens returns the full billable input. Tool-execution results are
// input by nature — Google describes them as "provided back to the model as
// input" — so they belong on the input line rather than being left out (which
// would drop them from the bill) or swept into the output derivation below
// (which would charge them at the output rate, typically several times higher).
func (m *usageMetadata) promptTokens() int {
	return m.PromptTokenCount + m.ToolUsePromptTokenCount
}

// completionTokens returns the full billable output — the answer plus any
// thinking tokens, which bill at the same output rate.
//
// Deriving it from the total is the only formula that holds on both backends.
// The Google AI endpoint already counts thinking inside candidatesTokenCount
// while Vertex AI reports the two side by side, so adding them unconditionally
// double-charges thinking on the former, and no field in the response says
// which convention is in force. Subtracting the input side sidesteps the
// question. Google defines the total as
//
//	prompt + candidates + tool_use_prompt + thoughts
//
// so total - (prompt + tool_use_prompt) leaves exactly the generated tokens
// under either convention. promptTokenCount already includes
// cachedContentTokenCount, so a cache hit doesn't distort the subtraction.
//
// The sum is the fallback for responses that omit the total (or report one at
// or below the input, which can't be right).
func (m *usageMetadata) completionTokens() int {
	if prompt := m.promptTokens(); m.TotalTokenCount > prompt {
		return m.TotalTokenCount - prompt
	}
	return m.CandidatesTokenCount + m.ThoughtsTokenCount
}

func (d *StreamDecoder) parseGeminiChunk(raw json.RawMessage) []protocols.IRStreamDelta {
	var chunk struct {
		UsageMetadata *usageMetadata `json:"usageMetadata"`
		ModelVersion  string         `json:"modelVersion"`
		Candidates    []struct {
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
		// A rejected block still emits a delta, marked Invalid. Emitting
		// nothing was the earlier behaviour and it is not enough: this decoder
		// rejects BEFORE IRUsage.Merge ever sees the frame, so the verdict
		// never reaches the accumulator — whatever an earlier valid frame
		// contributed stays in place, and the DeltaDone that follows completes
		// the stream and bills those stale counts as coherent.
		u, ok := chunk.UsageMetadata.toIRUsage()
		if !ok {
			u = protocols.IRUsage{Invalid: true}
		}
		deltas = append(deltas, protocols.DeltaUsage{Usage: u})
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
		UsageMetadata *usageMetadata `json:"usageMetadata"`
		ModelVersion  string         `json:"modelVersion"`
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
		// Leaves the zero-value usage (unknown, not zero-cost) when the block
		// is rejected — same treatment the streaming path gives it.
		if u, ok := resp.UsageMetadata.toIRUsage(); ok {
			irResp.Usage = u
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
