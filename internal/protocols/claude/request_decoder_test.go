package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/chat"
	"github.com/yolorouter/yolorouter/internal/protocols/responses"
)

// TestDecodeToolResult_ProducesBlockToolResult locks in a regression fix:
// tool_result on the Anthropic /v1/messages ingress must decode into
// protocols.BlockToolResult, not BlockText. Otherwise the egress encoders
// (the RoleTool branch in chat/gemini only recognizes BlockToolResult) can't
// retrieve the content, so the tool message sent upstream ends up with an
// empty content string and the model never receives the tool result (this
// exact failure occurred in production request 330480d1).
func TestDecodeToolResult_ProducesBlockToolResult(t *testing.T) {
	// content as an array (the form Claude Code actually sends in practice)
	bodyArray := []byte(`{
		"model": "x",
		"max_tokens": 100,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_1", "name": "read", "input": {"path": "a"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1",
				 "content": [{"type": "text", "text": "FILE CONTENTS HERE"}]}
			]}
		]
	}`)
	// content as a string, plus is_error
	bodyString := []byte(`{
		"model": "x",
		"max_tokens": 100,
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_2", "name": "fetch", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_2",
				 "content": "网络受限无法访问", "is_error": true}
			]}
		]
	}`)

	cases := []struct {
		name       string
		body       []byte
		wantSubstr string
		wantErr    bool
	}{
		{"array_content", bodyArray, "FILE CONTENTS HERE", false},
		{"string_content_with_error", bodyString, "网络受限无法访问", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ir, err := (RequestDecoder{}).DecodeRequest(tc.body, "x", false)
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}

			// 1) IR-level assertion: the tool message must carry BlockToolResult with non-empty content
			var toolMsg *protocols.IRMessage
			for i := range ir.Messages {
				if ir.Messages[i].Role == protocols.RoleTool {
					toolMsg = &ir.Messages[i]
					break
				}
			}
			if toolMsg == nil {
				t.Fatal("no RoleTool message was generated")
			}
			var tr *protocols.BlockToolResult
			for _, b := range toolMsg.Content {
				if br, ok := b.(protocols.BlockToolResult); ok {
					tr = &br
					break
				}
			}
			if tr == nil {
				t.Fatal("RoleTool message content is not BlockToolResult (regression: was incorrectly stored as BlockText)")
			}
			if tr.IsError != tc.wantErr {
				t.Errorf("is_error = %v, want %v", tr.IsError, tc.wantErr)
			}

			// 2) End-to-end assertion: after OpenAI chat egress encoding, the tool message content must be non-empty and contain the original text
			outBody, err := (chat.RequestEncoder{}).EncodeRequest(ir)
			if err != nil {
				t.Fatalf("encode failed: %v", err)
			}
			var out struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(outBody, &out); err != nil {
				t.Fatalf("failed to parse upstream request body: %v", err)
			}
			var found bool
			for _, m := range out.Messages {
				if m.Role == "tool" {
					found = true
					if m.Content == "" {
						t.Fatal("regression: OpenAI chat upstream tool message content is an empty string (tool result lost)")
					}
					if !strings.Contains(m.Content, tc.wantSubstr) {
						t.Errorf("chat upstream tool content = %q, want it to contain %q", m.Content, tc.wantSubstr)
					}
				}
			}
			if !found {
				t.Fatal("no role=tool message in the OpenAI chat upstream request body")
			}

			// 3) End-to-end assertion: after Responses egress encoding, function_call_output.output must be non-empty and contain the original text
			respBody, err := (responses.RequestEncoder{}).EncodeRequest(ir)
			if err != nil {
				t.Fatalf("responses encode failed: %v", err)
			}
			var respOut struct {
				Input []struct {
					Type   string `json:"type"`
					Output string `json:"output"`
				} `json:"input"`
			}
			if err := json.Unmarshal(respBody, &respOut); err != nil {
				t.Fatalf("failed to parse responses request body: %v", err)
			}
			var foundFCO bool
			for _, it := range respOut.Input {
				if it.Type == "function_call_output" {
					foundFCO = true
					if it.Output == "" {
						t.Fatal("regression: Responses upstream function_call_output.output is an empty string (tool result lost)")
					}
					if !strings.Contains(it.Output, tc.wantSubstr) {
						t.Errorf("responses output = %q, want it to contain %q", it.Output, tc.wantSubstr)
					}
				}
			}
			if !foundFCO {
				t.Fatal("no function_call_output item in the Responses request body")
			}
		})
	}
}
