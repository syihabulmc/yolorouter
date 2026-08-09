package errcode

import "testing"

// TestExistingSuccessCodeUnchanged pins the one code whose VALUE is a wire
// contract in its own right: every response envelope tells success from
// failure by code == 0, so Success moving off zero (or its message drifting)
// would break every client at once.
//
// Coherence between the code constants, ErrorMessages and the Err* sentinels
// is not asserted here per family any more — a structural check over the
// whole registry covers every declaration at once, so a newly added family
// is checked without anyone remembering to copy a test.
func TestExistingSuccessCodeUnchanged(t *testing.T) {
	if Success != 0 {
		t.Fatalf("Success code must remain 0, got %d", Success)
	}
	if ErrorMessages[Success] != "success" {
		t.Fatalf("Success message must remain \"success\", got %q", ErrorMessages[Success])
	}
}

// TestGetMessageCoversEveryRegisteredCode exercises GetMessage at runtime for
// every registered code — the structural registry check reads source, so a
// behavioural regression in this function (returning the fallback for a
// registered code, say) would not turn it red.
func TestGetMessageCoversEveryRegisteredCode(t *testing.T) {
	if len(ErrorMessages) == 0 {
		t.Fatal("ErrorMessages is empty; the check would pass by finding nothing")
	}
	for code, msg := range ErrorMessages {
		if got := GetMessage(code); got != msg {
			t.Errorf("GetMessage(%d) = %q, want %q", code, got, msg)
		}
	}
	if got := GetMessage(-1); got != "unknown error" {
		t.Errorf("GetMessage(unregistered) = %q, want the unknown-error fallback", got)
	}
}
