package chat

import (
	"encoding/json"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// anthropicStyleUsage is the IR usage produced by the claude decoder for an
// almost-fully-cached request: PromptTokens is NET (cache excluded) and
// CacheIncludedInPrompt is false. Emitting PromptTokens verbatim on an OpenAI
// egress would produce the invalid relation cached_tokens > prompt_tokens.
func anthropicStyleUsage() protocols.IRUsage {
	return protocols.IRUsage{
		PromptTokens:     0,
		CompletionTokens: 38,
		CacheWriteTokens: 344,
		CacheReadTokens:  6521,
		TotalTokens:      6903,
	}
}

// TestOpenAIResponseDecoder_DeepSeekCacheMissIsNotCacheWrite locks the DeepSeek
// usage mapping: it reports hit + miss where hit + miss == prompt_tokens, and
// "miss" is simply the uncached part of the prompt — not a cache-creation
// charge. Mapping miss to CacheWriteTokens made NetPromptTokens compute
// prompt - hit - miss == 0, so every DeepSeek request billed its whole input at
// cache-write price (unset, i.e. free, on most non-Anthropic models).
func TestOpenAIResponseDecoder_DeepSeekCacheMissIsNotCacheWrite(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-ds", "model": "deepseek-chat",
		"choices": [{"message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 10000, "completion_tokens": 5, "total_tokens": 10005,
			"prompt_cache_hit_tokens": 2000, "prompt_cache_miss_tokens": 8000
		}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.Usage.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens = %d, want 0 (DeepSeek has no cache-write concept)", resp.Usage.CacheWriteTokens)
	}
	if resp.Usage.CacheReadTokens != 2000 {
		t.Errorf("CacheReadTokens = %d, want 2000 (the hit portion)", resp.Usage.CacheReadTokens)
	}
	if got := resp.Usage.NetPromptTokens(); got != 8000 {
		t.Errorf("NetPromptTokens() = %d, want 8000 (the miss portion, billed at normal input price)", got)
	}
}

// TestOpenAIResponseDecoder_ReasoningTokensAreABreakdown locks reasoning_tokens
// as a breakdown OF completion_tokens (OpenAI counts reasoning inside the
// output total), so it must be recorded without being added on top — adding it
// would double-count output and over-bill. Round-trips back out through the
// encoder to confirm the detail survives instead of being dropped.
func TestOpenAIResponseDecoder_ReasoningTokensAreABreakdown(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-r", "model": "o3",
		"choices": [{"message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 100, "completion_tokens": 90, "total_tokens": 190,
			"completion_tokens_details": {"reasoning_tokens": 64}
		}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.Usage.CompletionTokens != 90 {
		t.Errorf("CompletionTokens = %d, want 90 (reasoning already inside, must not be added)", resp.Usage.CompletionTokens)
	}
	if resp.Usage.ReasoningTokens != 64 {
		t.Errorf("ReasoningTokens = %d, want 64", resp.Usage.ReasoningTokens)
	}

	out := ResponseEncoder{}.EncodeResponse(resp)
	var got struct {
		Usage struct {
			CompletionTokens        int `json:"completion_tokens"`
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Usage.CompletionTokens != 90 {
		t.Errorf("re-encoded completion_tokens = %d, want 90", got.Usage.CompletionTokens)
	}
	if got.Usage.CompletionTokensDetails == nil || got.Usage.CompletionTokensDetails.ReasoningTokens != 64 {
		t.Errorf("reasoning_tokens lost on the way out: %+v", got.Usage.CompletionTokensDetails)
	}
}

// TestOpenAIResponseEncoder_GrossPromptTokens locks the usage invariant
// on the non-streaming path: prompt_tokens must be the gross input (cache
// included), so cached_tokens stays a subset of it and
// total_tokens >= prompt_tokens + completion_tokens holds.
func TestOpenAIResponseEncoder_GrossPromptTokens(t *testing.T) {
	body := ResponseEncoder{}.EncodeResponse(&protocols.IRResponse{
		ID:         "msg_1",
		Model:      "claude-opus-4-8",
		Content:    "hi",
		StopReason: "stop",
		Usage:      anthropicStyleUsage(),
	})

	var got struct {
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Usage.PromptTokens != 6865 {
		t.Errorf("prompt_tokens = %d, want 6865 (0 net + 344 write + 6521 read)", got.Usage.PromptTokens)
	}
	if got.Usage.PromptTokensDetails == nil {
		t.Fatal("prompt_tokens_details missing")
	}
	if got.Usage.PromptTokensDetails.CachedTokens > got.Usage.PromptTokens {
		t.Errorf("invalid usage: cached_tokens %d > prompt_tokens %d",
			got.Usage.PromptTokensDetails.CachedTokens, got.Usage.PromptTokens)
	}
	if got.Usage.TotalTokens < got.Usage.PromptTokens+got.Usage.CompletionTokens {
		t.Errorf("invalid usage: total_tokens %d < prompt %d + completion %d",
			got.Usage.TotalTokens, got.Usage.PromptTokens, got.Usage.CompletionTokens)
	}
}

// TestOpenAIResponseEncoder_GrossUpstreamNotDoubleCounted guards the other
// direction: an upstream that already reports gross prompt tokens
// (CacheIncludedInPrompt=true) must pass through untouched, otherwise the
// cache would be added a second time.
func TestOpenAIResponseEncoder_GrossUpstreamNotDoubleCounted(t *testing.T) {
	body := ResponseEncoder{}.EncodeResponse(&protocols.IRResponse{
		ID:    "chatcmpl-1",
		Model: "gpt-4",
		Usage: protocols.IRUsage{
			PromptTokens:          2006,
			CompletionTokens:      10,
			CacheReadTokens:       1920,
			TotalTokens:           2016,
			CacheIncludedInPrompt: true,
		},
	})

	var got struct {
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Usage.PromptTokens != 2006 {
		t.Errorf("prompt_tokens = %d, want 2006 (already gross, must not re-add cache)", got.Usage.PromptTokens)
	}
}

// TestOpenAIStreamEncoder_UsageChunkAndDone covers the streaming path that
// previously emitted neither usage nor a terminator: with
// stream_options.include_usage=true the encoder must send one usage-only chunk
// (empty choices, populated usage) immediately before `data: [DONE]`.
func TestOpenAIStreamEncoder_UsageChunkAndDone(t *testing.T) {
	e := NewStreamEncoder()
	e.IncludeUsage = true
	e.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_1", Model: "claude-opus-4-8"},
		protocols.DeltaText{Text: "hi"},
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: anthropicStyleUsage()},
	})

	events := e.EncodeDone()
	if len(events) != 2 {
		t.Fatalf("EncodeDone returned %d events, want 2 (usage chunk + [DONE])", len(events))
	}
	if events[1].Data != "[DONE]" {
		t.Errorf("last event = %q, want [DONE]", events[1].Data)
	}

	var chunk struct {
		Object  string        `json:"object"`
		Choices []interface{} `json:"choices"`
		Usage   *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(events[0].Data), &chunk); err != nil {
		t.Fatalf("unmarshal usage chunk: %v", err)
	}
	if chunk.Object != "chat.completion.chunk" {
		t.Errorf("object = %q, want chat.completion.chunk", chunk.Object)
	}
	if chunk.Choices == nil || len(chunk.Choices) != 0 {
		t.Errorf("choices = %v, want empty array (the final usage chunk carries no choices)", chunk.Choices)
	}
	if chunk.Usage == nil {
		t.Fatal("usage missing from final chunk")
	}
	if chunk.Usage.PromptTokens != 6865 {
		t.Errorf("prompt_tokens = %d, want 6865 (gross)", chunk.Usage.PromptTokens)
	}
	if chunk.Usage.CompletionTokens != 38 {
		t.Errorf("completion_tokens = %d, want 38", chunk.Usage.CompletionTokens)
	}
	if chunk.Usage.PromptTokensDetails == nil || chunk.Usage.PromptTokensDetails.CachedTokens != 6521 {
		t.Errorf("cached_tokens missing or wrong: %+v", chunk.Usage.PromptTokensDetails)
	}
}

// TestOpenAIStreamEncoder_DoneWithoutIncludeUsage verifies the terminator is
// always sent, while the usage chunk stays opt-in: a client that did not ask
// for stream_options.include_usage must not receive an extra frame.
func TestOpenAIStreamEncoder_DoneWithoutIncludeUsage(t *testing.T) {
	e := NewStreamEncoder()
	e.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_1", Model: "claude-opus-4-8"},
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: anthropicStyleUsage()},
	})

	events := e.EncodeDone()
	if len(events) != 1 {
		t.Fatalf("EncodeDone returned %d events, want 1 ([DONE] only)", len(events))
	}
	if events[0].Data != "[DONE]" {
		t.Errorf("event = %q, want [DONE]", events[0].Data)
	}
}

// TestOpenAIStreamEncoder_RequiredChunkFields locks the fields the official
// OpenAPI lists as required on ChatCompletionChunk (choices, created, id,
// model, object). `created` was missing from every frame, which makes a
// strict-validating downstream reject the whole stream. All frames of one
// stream must also share the same created value, as real OpenAI does.
func TestOpenAIStreamEncoder_RequiredChunkFields(t *testing.T) {
	e := NewStreamEncoder()
	e.IncludeUsage = true
	events := e.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_1", Model: "claude-opus-4-8"},
		protocols.DeltaText{Text: "hi"},
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: anthropicStyleUsage()},
	})
	events = append(events, e.EncodeDone()...)

	var created float64
	seen := 0
	for _, ev := range events {
		if ev.Data == "[DONE]" {
			continue
		}
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			t.Fatalf("unmarshal %q: %v", ev.Data, err)
		}
		for _, field := range []string{"choices", "created", "id", "model", "object"} {
			if _, ok := chunk[field]; !ok {
				t.Errorf("chunk missing required field %q: %s", field, ev.Data)
			}
		}
		c, ok := chunk["created"].(float64)
		if !ok || c <= 0 {
			t.Errorf("created must be a positive unix timestamp, got %v", chunk["created"])
		}
		if seen == 0 {
			created = c
		} else if c != created {
			t.Errorf("created drifted within one stream: %v vs %v", c, created)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no chunks emitted")
	}
}

// TestOpenAIStreamEncoder_NonFinalChunksCarryNullUsage covers the wire rule
// that with stream_options.include_usage=true every chunk except the final
// usage-only one reports usage as null.
func TestOpenAIStreamEncoder_NonFinalChunksCarryNullUsage(t *testing.T) {
	e := NewStreamEncoder()
	e.IncludeUsage = true
	events := e.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_1", Model: "claude-opus-4-8"},
		protocols.DeltaText{Text: "hi"},
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: anthropicStyleUsage()},
	})

	for _, ev := range events {
		var chunk map[string]json.RawMessage
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		raw, ok := chunk["usage"]
		if !ok {
			t.Errorf("non-final chunk must carry an explicit usage key: %s", ev.Data)
			continue
		}
		if string(raw) != "null" {
			t.Errorf("non-final chunk usage = %s, want null", raw)
		}
	}

	// Without include_usage the key must stay absent entirely.
	plain := NewStreamEncoder()
	plainEvents := plain.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_1", Model: "m"},
		protocols.DeltaText{Text: "hi"},
	})
	for _, ev := range plainEvents {
		var chunk map[string]json.RawMessage
		_ = json.Unmarshal([]byte(ev.Data), &chunk)
		if _, ok := chunk["usage"]; ok {
			t.Errorf("usage key must be absent when include_usage was not requested: %s", ev.Data)
		}
	}
}

// TestOpenAIStreamEncoder_NoTerminatorOnTruncatedStream covers a truncated
// upstream that closes cleanly mid-stream: io.EOF is not a scanner error and
// the egress decoders report no error when they never saw a terminal frame, so
// EncodeDone still runs. It must stay silent — emitting [DONE] would hand the
// client an explicit success terminator for a half-written answer, which an
// OpenAI SDK accepts as complete instead of retrying. Mirrors the guards the
// gemini/responses/claude encoders already have.
func TestOpenAIStreamEncoder_NoTerminatorOnTruncatedStream(t *testing.T) {
	e := NewStreamEncoder()
	e.IncludeUsage = true
	// Content arrived, but no DeltaDone — the upstream died mid-answer.
	e.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_1", Model: "claude-opus-4-8"},
		protocols.DeltaText{Text: "half an ans"},
		protocols.DeltaUsage{Usage: anthropicStyleUsage()},
	})

	if events := e.EncodeDone(); len(events) != 0 {
		t.Fatalf("truncated stream must emit no terminator, got %d events: %+v", len(events), events)
	}
}

// TestOpenAIStreamEncoder_UsageChunkIDMatchesContentChunks locks the final
// usage frame to the same id as the content frames. Clients that group chunks
// by id (LiteLLM's stream_chunk_builder, Vercel AI SDK, chained gateways) drop
// a mismatched frame as a separate completion, which would silently defeat the
// whole point of emitting usage.
func TestOpenAIStreamEncoder_UsageChunkIDMatchesContentChunks(t *testing.T) {
	e := NewStreamEncoder()
	e.IncludeUsage = true
	// Claude egress hands over a native "msg_..." id, not a "chatcmpl-" one.
	contentEvents := e.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_01ABC", Model: "claude-opus-4-8"},
		protocols.DeltaText{Text: "hi"},
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: anthropicStyleUsage()},
	})

	idOf := func(raw string) string {
		var c struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatalf("unmarshal chunk: %v", err)
		}
		return c.ID
	}

	if len(contentEvents) == 0 {
		t.Fatal("expected content chunks")
	}
	contentID := idOf(contentEvents[0].Data)

	events := e.EncodeDone()
	if len(events) != 2 {
		t.Fatalf("want usage chunk + [DONE], got %d events", len(events))
	}
	if got := idOf(events[0].Data); got != contentID {
		t.Errorf("usage chunk id = %q, want %q (same as content chunks)", got, contentID)
	}
}

// TestOpenAIStreamEncoder_NoUsageChunkWhenUpstreamSilent guards against
// emitting an all-zero usage chunk when the upstream never reported usage —
// a downstream gateway would record a bogus 0-token request rather than
// treating the cost as unknown.
func TestOpenAIStreamEncoder_NoUsageChunkWhenUpstreamSilent(t *testing.T) {
	e := NewStreamEncoder()
	e.IncludeUsage = true
	e.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_1", Model: "gpt-4"},
		protocols.DeltaDone{StopReason: "stop"},
	})

	events := e.EncodeDone()
	if len(events) != 1 || events[0].Data != "[DONE]" {
		t.Fatalf("want [DONE] only when upstream reported no usage, got %d events: %+v", len(events), events)
	}
}

// TestFinishReason_TruncationOutranksToolCalls covers a run that emitted tool
// calls and was then cut off by max tokens. Reporting "tool_calls" would tell
// the client the accumulated arguments are complete and safe to execute, when a
// truncated argument stream may not even be valid JSON.
func TestFinishReason_TruncationOutranksToolCalls(t *testing.T) {
	cases := []struct {
		name         string
		reason       string
		hasToolCalls bool
		want         interface{}
	}{
		{"truncated mid tool call", "length", true, "length"},
		{"filtered mid tool call", "content_filter", true, "content_filter"},
		{"clean stop with tool calls", "stop", true, "tool_calls"},
		{"clean stop without tool calls", "stop", false, "stop"},
		{"truncated without tool calls", "length", false, "length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapFromOpenAIFinishReason(tc.reason, tc.hasToolCalls); got != tc.want {
				t.Errorf("mapFromOpenAIFinishReason(%q, %v) = %v, want %v",
					tc.reason, tc.hasToolCalls, got, tc.want)
			}
		})
	}
}

// TestMapToIRStopReason covers normalising OpenAI's finish_reason into the IR
// vocabulary. "function_call" is a deprecated but still legal Chat value, and a
// compatible upstream may invent its own; neither may reach the IR verbatim, or
// the abnormal-termination guard cannot classify it and downstream encoders
// emit values outside their own protocol's enum.
func TestMapToIRStopReason(t *testing.T) {
	cases := map[string]string{
		"stop":           "stop",
		"length":         "length",
		"tool_calls":     "tool_calls",
		"content_filter": "content_filter",
		"":               "",
		"function_call":  "tool_calls",
		"weird_vendor":   "weird_vendor",
	}
	for wire, want := range cases {
		if got := mapToIRStopReason(wire); got != want {
			t.Errorf("mapToIRStopReason(%q) = %q, want %q", wire, got, want)
		}
	}
	// Unknown values pass through verbatim (new-api's reasonmap does the same):
	// minting a synthetic marker would force every egress encoder to invent a
	// representation, and OpenAI's enum has none.
}

// TestOpenAIWireUsage_CacheWriteRoundTrips is the contract test for the
// non-standard cache-write alias: what openAIWireUsage emits must be exactly
// what ResponseDecoder reads back. OpenAI's schema has no cache-write field, so
// without the alias those tokens become indistinguishable from fresh input on
// the far side of an OpenAI hop and a downstream gateway bills them at the
// plain input rate instead of the cache-write rate.
//
// The round trip must also not double-count: prompt_tokens is gross (it already
// contains the cache write), so the decoded net input has to come back equal to
// the origin's net input, not inflated by the write portion.
func TestOpenAIWireUsage_CacheWriteRoundTrips(t *testing.T) {
	origin := anthropicStyleUsage()

	body, err := json.Marshal(map[string]interface{}{
		"id":    "chatcmpl-roundtrip",
		"model": "claude-opus-4-8",
		"choices": []interface{}{map[string]interface{}{
			"index":         0,
			"message":       map[string]interface{}{"role": "assistant", "content": "hi"},
			"finish_reason": "stop",
		}},
		"usage": openAIWireUsage(origin),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := ResponseDecoder{}.DecodeResponse(json.RawMessage(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got := decoded.Usage.CacheWriteTokens; got != origin.CacheWriteTokens {
		t.Errorf("CacheWriteTokens = %d, want %d — the alias did not survive the hop", got, origin.CacheWriteTokens)
	}
	if got := decoded.Usage.CacheReadTokens; got != origin.CacheReadTokens {
		t.Errorf("CacheReadTokens = %d, want %d", got, origin.CacheReadTokens)
	}
	if got := decoded.Usage.NetPromptTokens(); got != origin.NetPromptTokens() {
		t.Errorf("NetPromptTokens after round trip = %d, want %d — the write portion must be subtracted, not counted as fresh input", got, origin.NetPromptTokens())
	}
}

// TestChatDecoder_OpenRouterNestedCacheWrite covers the spelling a real
// upstream uses. OpenRouter documents a cache-WRITE count nested inside
// prompt_tokens_details beside cached_tokens; reading only the top-level
// Anthropic-style alias left CacheWriteTokens at 0 for every OpenRouter
// request, so those tokens were billed at the plain input rate.
func TestChatDecoder_OpenRouterNestedCacheWrite(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-or", "model": "anthropic/claude-opus-4.8",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 194, "completion_tokens": 2, "total_tokens": 196,
			"prompt_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 100, "audio_tokens": 0}
		}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Usage.CacheWriteTokens != 100 {
		t.Errorf("CacheWriteTokens = %d, want 100 from prompt_tokens_details.cache_write_tokens",
			resp.Usage.CacheWriteTokens)
	}
	// The write sits inside the gross prompt, so the net input excludes it.
	if got := resp.Usage.NetPromptTokens(); got != 94 {
		t.Errorf("NetPromptTokens() = %d, want 94 (194 gross - 100 write)", got)
	}
}

// TestChatDecoder_NestedCacheWriteWinsOverAlias pins the precedence rule. Both
// spellings name the same breakdown of prompt_tokens, so a decoder that added
// them would double-count; the documented nested field is the one that wins.
func TestChatDecoder_NestedCacheWriteWinsOverAlias(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-both", "model": "m",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 1000, "completion_tokens": 5, "total_tokens": 1005,
			"prompt_tokens_details": {"cache_write_tokens": 300},
			"cache_creation_input_tokens": 700
		}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Usage.CacheWriteTokens != 300 {
		t.Errorf("CacheWriteTokens = %d, want 300 (nested wins; 1000 would mean the two were summed)",
			resp.Usage.CacheWriteTokens)
	}
}

// TestOpenAIWireUsage_EmitsNestedCacheWriteOnly locks the emission policy:
// strict out, liberal in. Sending both spellings would put the same number in
// the payload twice and invite a downstream to add them.
func TestOpenAIWireUsage_EmitsNestedCacheWriteOnly(t *testing.T) {
	usage := openAIWireUsage(anthropicStyleUsage())

	if _, present := usage["cache_creation_input_tokens"]; present {
		t.Error("top-level alias must not be emitted alongside the nested field")
	}
	details, ok := usage["prompt_tokens_details"].(map[string]interface{})
	if !ok {
		t.Fatal("prompt_tokens_details missing")
	}
	if details["cache_write_tokens"] != 344 {
		t.Errorf("prompt_tokens_details.cache_write_tokens = %v, want 344", details["cache_write_tokens"])
	}
}

// TestChatDecoder_ExplicitZeroNestedCacheWriteBeatsAlias pins that precedence
// is by field PRESENCE, not by value. An upstream reporting the authoritative
// nested field as 0 is asserting there was no cache write; reading the field as
// a plain int made that indistinguishable from "absent" and silently billed a
// stale non-zero alias instead — at the cache-write rate, so the user was
// over-charged.
func TestChatDecoder_ExplicitZeroNestedCacheWriteBeatsAlias(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-zero", "model": "m",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 1000, "completion_tokens": 5, "total_tokens": 1005,
			"prompt_tokens_details": {"cache_write_tokens": 0},
			"cache_creation_input_tokens": 500
		}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Usage.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens = %d, want 0 (explicit nested zero must beat the alias)",
			resp.Usage.CacheWriteTokens)
	}
}
