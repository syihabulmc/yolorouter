package chat

import (
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestChatStreamEncoder_IncludeUsageEmitsUsageChunkBeforeDone is a
// regression test for FIX 6: for a cross-protocol OpenAI-ingress stream with
// stream_options.include_usage=true, the encoder merged DeltaUsage into
// e.usage but never emitted an SSE usage chunk before [DONE], so the caller
// got no usage. With IncludeUsage=true and a DeltaUsage carrying meaningful
// tokens, EncodeDone must emit a usage-bearing chunk before [DONE].
func TestChatStreamEncoder_IncludeUsageEmitsUsageChunkBeforeDone(t *testing.T) {
	enc := NewStreamEncoder()
	enc.IncludeUsage = true

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg1", Model: "gpt-4o"},
		protocols.DeltaText{Text: "hi"},
	})
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})

	events := enc.EncodeDone()
	if len(events) != 2 {
		t.Fatalf("EncodeDone() returned %d events, want 2 (usage chunk + [DONE]): %+v", len(events), events)
	}
	usageEvent := events[0].Data
	if !strings.Contains(usageEvent, `"prompt_tokens":10`) ||
		!strings.Contains(usageEvent, `"completion_tokens":5`) ||
		!strings.Contains(usageEvent, `"total_tokens":15`) {
		t.Errorf("usage chunk missing expected token counts: %s", usageEvent)
	}
	if !strings.Contains(usageEvent, `"object":"chat.completion.chunk"`) {
		t.Errorf("usage chunk missing chat.completion.chunk envelope: %s", usageEvent)
	}
	if !strings.Contains(usageEvent, `"choices":[]`) {
		t.Errorf("usage chunk must have empty choices, matching OpenAI's own final usage frame: %s", usageEvent)
	}
	doneEvent := events[1].Data
	if doneEvent != "[DONE]" {
		t.Errorf("second event = %q, want the [DONE] terminator following the usage chunk", doneEvent)
	}
}

// TestChatStreamEncoder_NoIncludeUsageEmitsNoUsageChunk is the negative
// counterpart: when the caller did not request stream_options.include_usage
// (IncludeUsage=false, the default), EncodeDone must emit only [DONE], with
// no usage chunk — preserving the pre-fix behavior for every existing
// caller that doesn't set the field.
func TestChatStreamEncoder_NoIncludeUsageEmitsNoUsageChunk(t *testing.T) {
	enc := NewStreamEncoder()
	// IncludeUsage left at its zero value (false).

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg1", Model: "gpt-4o"},
		protocols.DeltaText{Text: "hi"},
	})
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})

	events := enc.EncodeDone()
	if len(events) != 1 {
		t.Fatalf("EncodeDone() returned %d events, want 1 (just [DONE]): %+v", len(events), events)
	}
	if events[0].Data != "[DONE]" {
		t.Errorf("event = %q, want %q", events[0].Data, "[DONE]")
	}
}

// TestChatStreamEncoder_IncludeUsageWithoutUsageEmitsNoUsageChunk covers the
// guard against an empty (all-zero) usage chunk: IncludeUsage=true but the
// upstream never sent any DeltaUsage (or sent an all-zero one) must not
// produce a fabricated zero-usage chunk.
func TestChatStreamEncoder_IncludeUsageWithoutUsageEmitsNoUsageChunk(t *testing.T) {
	enc := NewStreamEncoder()
	enc.IncludeUsage = true

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg1", Model: "gpt-4o"},
		protocols.DeltaText{Text: "hi"},
	})

	events := enc.EncodeDone()
	if len(events) != 1 {
		t.Fatalf("EncodeDone() returned %d events, want 1 (just [DONE], no usage ever collected): %+v", len(events), events)
	}
	if events[0].Data != "[DONE]" {
		t.Errorf("event = %q, want %q", events[0].Data, "[DONE]")
	}
}
