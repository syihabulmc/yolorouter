package chat

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"testing"
)

// TestChatEncode_MultipleToolResultsSplit pins a regression: when a single
// RoleTool message carries multiple BlockToolResult blocks (e.g. a Gemini
// ingress merges several functionResponse parts from the same content into
// one message), Chat egress must split them into separate role=tool
// messages, each with its own tool_call_id — they must not be merged into
// one (otherwise call_id gets misaligned and tool results are lost).
func TestChatEncode_MultipleToolResultsSplit(t *testing.T) {
	ir := &protocols.IRRequest{
		Model: "x",
		Messages: []protocols.IRMessage{
			{
				Role: protocols.RoleTool,
				Content: []protocols.IRContentBlock{
					protocols.BlockToolResult{ToolUseID: "call_A", Content: json.RawMessage(`"RESULT_A"`)},
					protocols.BlockToolResult{ToolUseID: "call_B", Content: json.RawMessage(`"RESULT_B"`)},
				},
			},
		},
	}
	body, err := (RequestEncoder{}).EncodeRequest(ir)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	var out struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	got := map[string]string{}
	for _, m := range out.Messages {
		if m.Role == "tool" {
			got[m.ToolCallID] = m.Content
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 separate tool messages, got %d: %+v", len(got), got)
	}
	if got["call_A"] != "RESULT_A" {
		t.Errorf("call_A content = %q, want RESULT_A", got["call_A"])
	}
	if got["call_B"] != "RESULT_B" {
		t.Errorf("call_B content = %q, want RESULT_B", got["call_B"])
	}
}

// TestChatEncode_ToolResultObjectContent verifies that a Gemini-style
// structured functionResponse object (not a JSON string) still produces a
// non-empty content field through Chat egress, falling back to the raw JSON.
func TestChatEncode_ToolResultObjectContent(t *testing.T) {
	ir := &protocols.IRRequest{
		Model: "x",
		Messages: []protocols.IRMessage{
			{
				Role: protocols.RoleTool,
				Content: []protocols.IRContentBlock{
					protocols.BlockToolResult{ToolUseID: "call_obj", Content: json.RawMessage(`{"temperature":25}`)},
				},
			},
		},
	}
	body, err := (RequestEncoder{}).EncodeRequest(ir)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	var out struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var found bool
	for _, m := range out.Messages {
		if m.Role == "tool" {
			found = true
			if m.Content == "" {
				t.Fatal("regression: object-typed tool result produced empty content through Chat egress")
			}
		}
	}
	if !found {
		t.Fatal("no role=tool message found")
	}
}
