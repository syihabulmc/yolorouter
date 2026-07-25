package compress

import (
	"bytes"
	"testing"
)

func TestLocateClaudeLiveZoneToolResult(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"BIG OUTPUT HERE"}]}` +
		`]}`)
	blocks := locateClaudeLiveZone(body)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 tool_result block, got %d", len(blocks))
	}
	if blocks[0].Text != "BIG OUTPUT HERE" {
		t.Fatalf("unexpected text: %q", blocks[0].Text)
	}
	// Range must begin exactly at the value's opening quote.
	if body[blocks[0].Range[0]] != '"' {
		t.Fatal("range start must be the value's opening quote")
	}
	// The range must cover exactly the quoted literal.
	if string(body[blocks[0].Range[0]:blocks[0].Range[1]]) != `"BIG OUTPUT HERE"` {
		t.Fatalf("range literal mismatch: %q", string(body[blocks[0].Range[0]:blocks[0].Range[1]]))
	}
}

func TestLocateClaudeSkipsAssistantAndOldUser(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"OLD"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"reply"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"new","content":"NEW"}]}` +
		`]}`)
	blocks := locateClaudeLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "NEW" {
		t.Fatalf("only the latest user tool_result should be collected, got %+v", blocks)
	}
}

// tool_result.content may itself be an array of text blocks.
func TestLocateClaudeToolResultArrayText(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":[{"type":"text","text":"go"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"ARRAY OUT"}]}]}` +
		`]}`)
	blocks := locateClaudeLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "ARRAY OUT" {
		t.Fatalf("expected to collect tool_result.content[].text, got %+v", blocks)
	}
	if string(body[blocks[0].Range[0]:blocks[0].Range[1]]) != `"ARRAY OUT"` {
		t.Fatalf("range literal mismatch: %q", string(body[blocks[0].Range[0]:blocks[0].Range[1]]))
	}
}

// Field order independence: type must still be recognized when it appears after content.
func TestLocateClaudeOrderIndependent(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":[{"content":"REORDERED","tool_use_id":"t","type":"tool_result"}]}` +
		`]}`)
	blocks := locateClaudeLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "REORDERED" {
		t.Fatalf("tool_result must be recognized even with reordered fields, got %+v", blocks)
	}
}

// A plain string user content (direct user input) is not a compression target.
func TestLocateClaudePlainUserStringNotCompressed(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"just my question"}` +
		`]}`)
	blocks := locateClaudeLiveZone(body)
	if len(blocks) != 0 {
		t.Fatalf("plain user input should not be collected, got %+v", blocks)
	}
}

func TestLocateClaudeMalformedIsNil(t *testing.T) {
	if blocks := locateClaudeLiveZone([]byte(`{not json`)); blocks != nil {
		t.Fatalf("malformed JSON should return nil, got %+v", blocks)
	}
	if blocks := locateClaudeLiveZone([]byte(`{"foo":1}`)); blocks != nil {
		t.Fatalf("missing messages array should return nil, got %+v", blocks)
	}
}

// Block-level frozen boundary: when two messages share the same text, only
// the one after the last assistant (the live zone) is collected.
func TestClaudeBlockLevelFrozenBoundary(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"SAME"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"ok"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"SAME"}]}` +
		`]}`)
	blocks := locateClaudeLiveZone(body)
	if len(blocks) != 1 {
		t.Fatalf("only the live-zone copy of identical text should be collected, got %d", len(blocks))
	}
	// The hit must be the second SAME (larger offset), i.e. after the assistant boundary.
	if blocks[0].Range[0] < bytes.Index(body, []byte("assistant")) {
		t.Fatal("collected range must lie after the assistant boundary")
	}
}

// ---- chat entry point ----

func TestLocateChatLiveZoneToolMessage(t *testing.T) {
	body := []byte(`{"model":"gpt","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"calling","tool_calls":[{"id":"c1"}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"TOOL OUTPUT"}` +
		`]}`)
	blocks := locateChatLiveZone(body)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 tool message content block, got %d", len(blocks))
	}
	if blocks[0].Text != "TOOL OUTPUT" {
		t.Fatalf("unexpected text: %q", blocks[0].Text)
	}
	if body[blocks[0].Range[0]] != '"' {
		t.Fatal("range start must be the value's opening quote")
	}
	if string(body[blocks[0].Range[0]:blocks[0].Range[1]]) != `"TOOL OUTPUT"` {
		t.Fatalf("range literal mismatch: %q", string(body[blocks[0].Range[0]:blocks[0].Range[1]]))
	}
}

func TestLocateChatContentArrayText(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":"x"},` +
		`{"role":"user","content":[{"type":"text","text":"USER TEXT"}]}` +
		`]}`)
	blocks := locateChatLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "USER TEXT" {
		t.Fatalf("expected to collect content[].text, got %+v", blocks)
	}
}

func TestLocateChatSkipsAssistant(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"tool","content":"OLD"},` +
		`{"role":"assistant","content":"reply"},` +
		`{"role":"tool","content":"NEW"}` +
		`]}`)
	blocks := locateChatLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "NEW" {
		t.Fatalf("only tool messages after the last assistant should be collected, got %+v", blocks)
	}
}

func TestLocateChatMalformedIsNil(t *testing.T) {
	if blocks := locateChatLiveZone([]byte(`{bad`)); blocks != nil {
		t.Fatalf("malformed JSON should return nil, got %+v", blocks)
	}
}

// --- Responses API ---

func TestLocateResponsesFunctionCallOutput(t *testing.T) {
	body := []byte(`{"input":[` +
		`{"role":"user","content":"run the tests"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"bash","input":{"command":"go test"}}]},` +
		`{"type":"function_call_output","call_id":"c1","output":"--- FAIL: TestFoo\nFAIL"}` +
		`]}`)
	blocks := locateResponsesLiveZone(body)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 function_call_output.output block, got %d", len(blocks))
	}
	if blocks[0].Text != "--- FAIL: TestFoo\nFAIL" {
		t.Fatalf("unexpected text: %q", blocks[0].Text)
	}
}

func TestLocateResponsesUserMessageContent(t *testing.T) {
	body := []byte(`{"input":[` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"PASS\ntests done"}` +
		`]}`)
	blocks := locateResponsesLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "PASS\ntests done" {
		t.Fatalf("expected to collect the live-zone user content, got %+v", blocks)
	}
}

func TestLocateResponsesUserInputFrozenBeforeAssistant(t *testing.T) {
	// User input that appears before the assistant is frozen and must not be collected.
	body := []byte(`{"input":[` +
		`{"role":"user","content":"OLD INPUT"},` +
		`{"role":"assistant","content":"reply"},` +
		`{"type":"function_call_output","call_id":"c1","output":"NEW OUTPUT"}` +
		`]}`)
	blocks := locateResponsesLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "NEW OUTPUT" {
		t.Fatalf("only content after the last assistant should be collected, got %+v", blocks)
	}
}

func TestLocateResponsesInputTextContentBlock(t *testing.T) {
	// The Responses API user message uses type:input_text rather than type:text.
	body := []byte(`{"input":[` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":[{"type":"input_text","text":"BUILD OUTPUT HERE"}]}` +
		`]}`)
	blocks := locateResponsesLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "BUILD OUTPUT HERE" {
		t.Fatalf("expected to recognize the input_text content block, got %+v", blocks)
	}
}

func TestLocateResponsesMalformedIsNil(t *testing.T) {
	if blocks := locateResponsesLiveZone([]byte(`{bad`)); blocks != nil {
		t.Fatalf("malformed JSON should return nil, got %+v", blocks)
	}
	if blocks := locateResponsesLiveZone([]byte(`{"messages":[]}`)); blocks != nil {
		t.Fatalf("missing input key should return nil, got %+v", blocks)
	}
}

// ---- Gemini API ----

func TestLocateGeminiLiveZoneBasic(t *testing.T) {
	body := []byte(`{"contents":[` +
		`{"role":"user","parts":[{"text":"USER QUERY"}]},` +
		`{"role":"model","parts":[{"text":"ok"}]},` +
		`{"role":"user","parts":[{"text":"TOOL OUTPUT HERE"}]}` +
		`]}`)
	blocks := locateGeminiLiveZone(body)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 live-zone block, got %d", len(blocks))
	}
	if blocks[0].Text != "TOOL OUTPUT HERE" {
		t.Fatalf("unexpected text: %q", blocks[0].Text)
	}
}

func TestLocateGeminiLiveZoneFrozenBeforeModel(t *testing.T) {
	body := []byte(`{"contents":[` +
		`{"role":"user","parts":[{"text":"FROZEN"}]},` +
		`{"role":"model","parts":[{"text":"reply"}]},` +
		`{"role":"user","parts":[{"text":"LIVE"}]}` +
		`]}`)
	blocks := locateGeminiLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "LIVE" {
		t.Fatalf("only the user block after the last model should be collected, got %+v", blocks)
	}
}

func TestLocateGeminiLiveZoneEmptyRoleAsUser(t *testing.T) {
	// Role is omitempty; an absent role is treated as user.
	body := []byte(`{"contents":[` +
		`{"role":"model","parts":[{"text":"reply"}]},` +
		`{"parts":[{"text":"NO ROLE = USER"}]}` +
		`]}`)
	blocks := locateGeminiLiveZone(body)
	if len(blocks) != 1 || blocks[0].Text != "NO ROLE = USER" {
		t.Fatalf("message with omitted role should be treated as user, got %+v", blocks)
	}
}

func TestLocateGeminiLiveZoneThoughtPartSkipped(t *testing.T) {
	// Parts with thought:true must not enter the compression path.
	body := []byte(`{"contents":[` +
		`{"role":"model","parts":[{"text":"reply"}]},` +
		`{"role":"user","parts":[{"thought":true,"text":"THINKING"},{"text":"ACTUAL OUTPUT"}]}` +
		`]}`)
	blocks := locateGeminiLiveZone(body)
	for _, b := range blocks {
		if b.Text == "THINKING" {
			t.Fatal("thought:true parts must not be collected")
		}
	}
	found := false
	for _, b := range blocks {
		if b.Text == "ACTUAL OUTPUT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the non-thought text part should still be collected, got %+v", blocks)
	}
}

func TestLocateGeminiNullRoleNotTreatedAsUser(t *testing.T) {
	body := []byte(`{"contents":[` +
		`{"role":"model","parts":[{"text":"ok"}]},` +
		`{"role":null,"parts":[{"text":"SHOULD SKIP"}]},` +
		`{"role":"user","parts":[{"text":"SHOULD INCLUDE"}]}` +
		`]}`)
	blocks := locateGeminiLiveZone(body)
	for _, b := range blocks {
		if b.Text == "SHOULD SKIP" {
			t.Fatal("a null role must not be treated as user")
		}
	}
	found := false
	for _, b := range blocks {
		if b.Text == "SHOULD INCLUDE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the real user role should be collected, got %+v", blocks)
	}
}

func TestLocateGeminiLiveZoneMalformedIsNil(t *testing.T) {
	if blocks := locateGeminiLiveZone([]byte(`{bad`)); blocks != nil {
		t.Fatalf("malformed JSON should return nil, got %+v", blocks)
	}
	if blocks := locateGeminiLiveZone([]byte(`{"messages":[]}`)); blocks != nil {
		t.Fatalf("missing contents key should return nil, got %+v", blocks)
	}
}
