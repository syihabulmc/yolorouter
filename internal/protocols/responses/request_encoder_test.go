package responses

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"strings"
	"testing"
)

// TestEncodeResponsesInput_MultipleToolResults locks in a regression fix:
// when a single RoleTool message carries multiple BlockToolResult blocks
// (e.g. the Gemini ingress merges several functionResponse entries from the
// same content into one message), Responses egress must generate one
// function_call_output per tool result, each with its own call_id; they must
// not be merged into a single call_id (which would drop output and misalign
// results).
func TestEncodeResponsesInput_MultipleToolResults(t *testing.T) {
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
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	got := map[string]string{}
	for _, it := range out.Input {
		if it.Type == "function_call_output" {
			got[it.CallID] = it.Output
		}
	}
	if len(got) != 2 {
		t.Fatalf("want 2 independent function_call_output items, got %d: %+v", len(got), got)
	}
	if got["call_A"] != "RESULT_A" {
		t.Errorf("call_A output = %q, want RESULT_A", got["call_A"])
	}
	if got["call_B"] != "RESULT_B" {
		t.Errorf("call_B output = %q, want RESULT_B", got["call_B"])
	}
}

// TestEncodeResponsesInput_ObjectContent verifies that a Gemini-style structured
// functionResponse object (not a JSON string, e.g. {"temperature":25}) produces
// a non-empty output via Responses egress, falling back to the raw JSON.
func TestEncodeResponsesInput_ObjectContent(t *testing.T) {
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
		Input []struct {
			Type   string `json:"type"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var found bool
	for _, it := range out.Input {
		if it.Type == "function_call_output" {
			found = true
			if it.Output == "" {
				t.Fatal("regression: output is empty after Responses egress for an object-typed tool result")
			}
			if !strings.Contains(it.Output, "temperature") {
				t.Errorf("output = %q, want it to contain the original JSON field temperature", it.Output)
			}
		}
	}
	if !found {
		t.Fatal("no function_call_output item found")
	}
}

// TestEncodeResponsesInput_EmptyStringContent locks in a regression fix:
// a legitimate empty-string tool result (Claude tool_result.content:"" is
// normalized to the JSON string "") must produce the empty string "" as
// output, not be rewritten by the fallback logic into a literal JSON value
// (it once regressed to the two-character string "\"\"").
func TestEncodeResponsesInput_EmptyStringContent(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty_json_string", `""`, ""},
		{"json_null", `null`, ""},
		{"normal_string", `"hello"`, "hello"},
		{"object", `{"k":1}`, `{"k":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ir := &protocols.IRRequest{
				Model: "x",
				Messages: []protocols.IRMessage{
					{
						Role: protocols.RoleTool,
						Content: []protocols.IRContentBlock{
							protocols.BlockToolResult{ToolUseID: "c1", Content: json.RawMessage(tc.raw)},
						},
					},
				},
			}
			body, err := (RequestEncoder{}).EncodeRequest(ir)
			if err != nil {
				t.Fatalf("encode failed: %v", err)
			}
			var out struct {
				Input []struct {
					Type   string `json:"type"`
					Output string `json:"output"`
				} `json:"input"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if len(out.Input) != 1 {
				t.Fatalf("want 1 item, got %d", len(out.Input))
			}
			if out.Input[0].Output != tc.want {
				t.Errorf("output = %q, want %q", out.Input[0].Output, tc.want)
			}
		})
	}
}

// TestEncodeResponsesInput_ToolResultFallback verifies a defensive fallback:
// in the extreme case where a RoleTool message has no BlockToolResult (only
// BlockText), the encoder should still synthesize a function_call_output
// using msg.ToolCallID, so the tool message is never dropped.
func TestEncodeResponsesInput_ToolResultFallback(t *testing.T) {
	ir := &protocols.IRRequest{
		Model: "x",
		Messages: []protocols.IRMessage{
			{
				Role:       protocols.RoleTool,
				ToolCallID: "call_legacy",
				Content:    []protocols.IRContentBlock{protocols.BlockText{Text: "LEGACY_TEXT"}},
			},
		},
	}
	body, err := (RequestEncoder{}).EncodeRequest(ir)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	var out struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(out.Input) != 1 || out.Input[0].Type != "function_call_output" {
		t.Fatalf("want 1 function_call_output, got: %+v", out.Input)
	}
	if out.Input[0].CallID != "call_legacy" || out.Input[0].Output != "LEGACY_TEXT" {
		t.Errorf("fallback item = %+v, want call_legacy/LEGACY_TEXT", out.Input[0])
	}
}
