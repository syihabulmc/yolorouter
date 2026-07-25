package errcode

import "testing"

func TestNewRouteCodesAreUniqueAndRegistered(t *testing.T) {
	newCodes := []int{RouteNotFound, MethodNotAllowed, RequestEntityTooLarge, InternalError}
	seen := map[int]bool{}
	for _, code := range newCodes {
		if code == 0 {
			t.Fatalf("code must not be zero value")
		}
		if seen[code] {
			t.Fatalf("duplicate code value: %d", code)
		}
		seen[code] = true
		if _, ok := ErrorMessages[code]; !ok {
			t.Fatalf("code %d has no entry in ErrorMessages", code)
		}
	}
}

func TestExistingSuccessCodeUnchanged(t *testing.T) {
	if Success != 0 {
		t.Fatalf("Success code must remain 0, got %d", Success)
	}
	if ErrorMessages[Success] != "success" {
		t.Fatalf("Success message must remain \"success\", got %q", ErrorMessages[Success])
	}
}

func TestProviderKeyErrorCodesHaveMessagesAndSentinels(t *testing.T) {
	cases := []struct {
		code int
		err  error
	}{
		{ProviderKeyNotFound, ErrProviderKeyNotFound},
		{ProviderKeyLabelTaken, ErrProviderKeyLabelTaken},
		{ProviderKeyNotVerified, ErrProviderKeyNotVerified},
		{ProviderKeyNeedsReentry, ErrProviderKeyNeedsReentry},
	}
	for _, c := range cases {
		msg, ok := ErrorMessages[c.code]
		if !ok || msg == "" {
			t.Fatalf("code %d: missing ErrorMessages entry", c.code)
		}
		if c.err == nil || c.err.Error() != msg {
			t.Fatalf("code %d: sentinel error text %q does not match ErrorMessages %q", c.code, c.err, msg)
		}
	}
}

func TestCustomSystemPromptErrorCodesRegistered(t *testing.T) {
	cases := []struct {
		code int
		err  error
	}{
		{CustomSystemPromptTooLong, ErrCustomSystemPromptTooLong},
		{CustomSystemPromptEmpty, ErrCustomSystemPromptEmpty},
		{CustomSystemPromptConflict, ErrCustomSystemPromptConflict},
	}
	for _, c := range cases {
		msg, ok := ErrorMessages[c.code]
		if !ok || msg == "" {
			t.Fatalf("code %d: missing ErrorMessages entry", c.code)
		}
		// sentinel text must equal the map's single source of truth
		if c.err == nil || c.err.Error() != msg {
			t.Fatalf("code %d: sentinel error text %q does not match ErrorMessages %q", c.code, c.err, msg)
		}
		if GetMessage(c.code) == "unknown error" {
			t.Fatalf("code %d: GetMessage returned unknown", c.code)
		}
	}
}

func TestInputCompressionConflictCodeRegistered(t *testing.T) {
	if InputCompressionConflict != 11014 {
		t.Fatalf("InputCompressionConflict must be 11014, got %d", InputCompressionConflict)
	}
	msg, ok := ErrorMessages[InputCompressionConflict]
	if !ok || msg == "" {
		t.Fatalf("code %d: missing ErrorMessages entry", InputCompressionConflict)
	}
	if ErrInputCompressionConflict == nil || ErrInputCompressionConflict.Error() != msg {
		t.Fatalf("sentinel error text %q does not match ErrorMessages %q", ErrInputCompressionConflict, msg)
	}
	if GetMessage(InputCompressionConflict) == "unknown error" {
		t.Fatalf("code %d: GetMessage returned unknown", InputCompressionConflict)
	}
}

func TestCompressEnabledRequiredCodeRegistered(t *testing.T) {
	if CompressEnabledRequired != 11015 {
		t.Fatalf("CompressEnabledRequired must be 11015, got %d", CompressEnabledRequired)
	}
	msg, ok := ErrorMessages[CompressEnabledRequired]
	if !ok || msg == "" {
		t.Fatalf("code %d: missing ErrorMessages entry", CompressEnabledRequired)
	}
	if ErrCompressEnabledRequired == nil || ErrCompressEnabledRequired.Error() != msg {
		t.Fatalf("sentinel error text %q does not match ErrorMessages %q", ErrCompressEnabledRequired, msg)
	}
	if GetMessage(CompressEnabledRequired) == "unknown error" {
		t.Fatalf("code %d: GetMessage returned unknown", CompressEnabledRequired)
	}
}

func TestModelErrorCodesAreUniqueWithMessagesAndSentinels(t *testing.T) {
	cases := []struct {
		code int
		err  error
	}{
		{ModelNotFound, ErrModelNotFound},
		{ModelNameTaken, ErrModelNameTaken},
		{ModelCandidateNotFound, ErrModelCandidateNotFound},
		{ModelCandidateProviderTaken, ErrModelCandidateProviderTaken},
		{ModelCandidateNotVerified, ErrModelCandidateNotVerified},
	}
	seen := map[int]bool{}
	for _, c := range cases {
		if seen[c.code] {
			t.Fatalf("duplicate code value: %d", c.code)
		}
		seen[c.code] = true
		msg, ok := ErrorMessages[c.code]
		if !ok || msg == "" {
			t.Fatalf("code %d: missing ErrorMessages entry", c.code)
		}
		if c.err == nil || c.err.Error() != msg {
			t.Fatalf("code %d: sentinel error text %q does not match ErrorMessages %q", c.code, c.err, msg)
		}
	}
}
