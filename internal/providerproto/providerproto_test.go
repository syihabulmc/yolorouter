package providerproto

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

func TestValidateType(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "empty normalizes to openai", in: "", want: "openai"},
		{name: "openai as-is", in: "openai", want: "openai"},
		{name: "anthropic as-is", in: "anthropic", want: "anthropic"},
		{name: "gemini as-is", in: "gemini", want: "gemini"},
		{name: "responses as-is", in: "responses", want: "responses"},
		{name: "unknown value claude", in: "claude", wantErr: true},
		{name: "unknown value foo", in: "foo", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateType(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateType(%q): expected error, got nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateType(%q): unexpected error: %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Fatalf("ValidateType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateEndpoints(t *testing.T) {
	t.Run("empty string is valid and stays empty", func(t *testing.T) {
		got, err := ValidateEndpoints("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty string", got)
		}
	})

	t.Run("empty object canonicalizes to empty string", func(t *testing.T) {
		// "{}" and "" both mean "no additional protocols"; they must
		// normalize to the same stored form so a name-only edit that
		// re-submits the empty config never spuriously bumps
		// destination_version.
		got, err := ValidateEndpoints("{}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty string (empty object canonicalized)", got)
		}
	})

	t.Run("single reuse-base-url entry", func(t *testing.T) {
		got, err := ValidateEndpoints(`{"anthropic":""}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := `{"anthropic":""}`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("single explicit URL entry", func(t *testing.T) {
		got, err := ValidateEndpoints(`{"responses":"https://gw/v1"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := `{"responses":"https://gw/v1"}`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("multiple entries normalized with sorted keys", func(t *testing.T) {
		got, err := ValidateEndpoints(`{"responses":"https://gw/v1","anthropic":""}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := `{"anthropic":"","responses":"https://gw/v1"}`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("malformed JSON rejected", func(t *testing.T) {
		if _, err := ValidateEndpoints(`{not json`); err == nil {
			t.Fatal("expected error for malformed JSON, got nil")
		}
	})

	t.Run("JSON array rejected", func(t *testing.T) {
		if _, err := ValidateEndpoints(`["anthropic"]`); err == nil {
			t.Fatal("expected error for non-object JSON, got nil")
		}
	})

	t.Run("unknown protocol key rejected", func(t *testing.T) {
		if _, err := ValidateEndpoints(`{"claude":""}`); err == nil {
			t.Fatal("expected error for unknown protocol key, got nil")
		}
	})

	t.Run("non-URL value rejected", func(t *testing.T) {
		if _, err := ValidateEndpoints(`{"anthropic":"not-a-url"}`); err == nil {
			t.Fatal("expected error for non-URL value, got nil")
		}
	})

	t.Run("bad scheme rejected", func(t *testing.T) {
		if _, err := ValidateEndpoints(`{"anthropic":"ftp://x"}`); err == nil {
			t.Fatal("expected error for bad scheme, got nil")
		}
	})

	t.Run("non-string value rejected", func(t *testing.T) {
		if _, err := ValidateEndpoints(`{"anthropic":123}`); err == nil {
			t.Fatal("expected error for non-string value, got nil")
		}
	})

	t.Run("normalized output is byte-stable regardless of key order", func(t *testing.T) {
		gotA, errA := ValidateEndpoints(`{"gemini":"https://g","anthropic":"","responses":"https://r"}`)
		gotB, errB := ValidateEndpoints(`{"responses":"https://r","gemini":"https://g","anthropic":""}`)
		if errA != nil || errB != nil {
			t.Fatalf("unexpected errors: %v, %v", errA, errB)
		}
		if gotA != gotB {
			t.Fatalf("normalized outputs differ: %q vs %q", gotA, gotB)
		}
	})
}

func TestSupportedSet(t *testing.T) {
	cases := []struct {
		name              string
		providerType      string
		protocolEndpoints string
		want              map[string]bool
	}{
		{
			name:         "openai with no extra protocols",
			providerType: "openai",
			want:         map[string]bool{"openai": true},
		},
		{
			name:         "anthropic with no extra protocols",
			providerType: "anthropic",
			want:         map[string]bool{"anthropic": true},
		},
		{
			name:              "openai with anthropic endpoint",
			providerType:      "openai",
			protocolEndpoints: `{"anthropic":""}`,
			want:              map[string]bool{"openai": true, "anthropic": true},
		},
		{
			name:              "anthropic with gemini endpoint",
			providerType:      "anthropic",
			protocolEndpoints: `{"gemini":"https://g"}`,
			want:              map[string]bool{"anthropic": true, "gemini": true},
		},
		{
			name: "both empty normalizes to openai",
			want: map[string]bool{"openai": true},
		},
		{
			name:              "malformed endpoints JSON ignored",
			providerType:      "openai",
			protocolEndpoints: `{not json`,
			want:              map[string]bool{"openai": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SupportedSet(tc.providerType, tc.protocolEndpoints)
			if len(got) != len(tc.want) {
				t.Fatalf("SupportedSet(%q, %q) = %v, want %v", tc.providerType, tc.protocolEndpoints, got, tc.want)
			}
			for k := range tc.want {
				if !got[protocols.ProtocolID(k)] {
					t.Fatalf("SupportedSet(%q, %q) = %v, missing key %q", tc.providerType, tc.protocolEndpoints, got, k)
				}
			}
		})
	}
}

func TestVerificationTargets(t *testing.T) {
	t.Run("openai-only provider has one target at baseURL", func(t *testing.T) {
		got := VerificationTargets("openai", "", "https://api.openai.com/v1")
		want := []VerificationTarget{{Proto: protocols.ProtocolOpenAI, URL: "https://api.openai.com/v1"}}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("VerificationTargets(openai-only) = %+v, want %+v", got, want)
		}
	})

	t.Run("empty provider type normalizes to openai with one target", func(t *testing.T) {
		got := VerificationTargets("", "", "https://api.example.com")
		want := []VerificationTarget{{Proto: protocols.ProtocolOpenAI, URL: "https://api.example.com"}}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("VerificationTargets(empty type) = %+v, want %+v", got, want)
		}
	})

	t.Run("openai primary with anthropic endpoint yields two targets primary first", func(t *testing.T) {
		got := VerificationTargets("openai", `{"anthropic":"https://B/v1"}`, "https://A/v1")
		want := []VerificationTarget{
			{Proto: protocols.ProtocolOpenAI, URL: "https://A/v1"},
			{Proto: protocols.ProtocolClaude, URL: "https://B/v1"},
		}
		if len(got) != len(want) {
			t.Fatalf("VerificationTargets(openai+anthropic) = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("VerificationTargets(openai+anthropic)[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("empty endpoint value resolves to baseURL", func(t *testing.T) {
		got := VerificationTargets("openai", `{"anthropic":""}`, "https://A/v1")
		want := []VerificationTarget{
			{Proto: protocols.ProtocolOpenAI, URL: "https://A/v1"},
			{Proto: protocols.ProtocolClaude, URL: "https://A/v1"},
		}
		if len(got) != len(want) {
			t.Fatalf("VerificationTargets(empty endpoint value) = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("VerificationTargets(empty endpoint value)[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("bad JSON falls back to just the primary target", func(t *testing.T) {
		got := VerificationTargets("openai", `{not json`, "https://A/v1")
		want := []VerificationTarget{{Proto: protocols.ProtocolOpenAI, URL: "https://A/v1"}}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("VerificationTargets(bad JSON) = %+v, want %+v", got, want)
		}
	})

	t.Run("multiple extra protocols sorted deterministically after primary", func(t *testing.T) {
		got := VerificationTargets("openai", `{"responses":"https://R","anthropic":"https://B","gemini":"https://G"}`, "https://A/v1")
		want := []VerificationTarget{
			{Proto: protocols.ProtocolOpenAI, URL: "https://A/v1"},
			{Proto: protocols.ProtocolClaude, URL: "https://B"},
			{Proto: protocols.ProtocolGemini, URL: "https://G"},
			{Proto: protocols.ProtocolResponses, URL: "https://R"},
		}
		if len(got) != len(want) {
			t.Fatalf("VerificationTargets(multi) = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("VerificationTargets(multi)[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}

// TestTypeOfLenientFallback pins the read-path normalization: rows that
// predate the provider_type column ("") and values no release ever wrote
// both resolve to OpenAI instead of failing a read.
func TestTypeOfLenientFallback(t *testing.T) {
	cases := []struct {
		in   string
		want protocols.ProtocolID
	}{
		{"", protocols.ProtocolOpenAI},
		{"openai", protocols.ProtocolOpenAI},
		{"anthropic", protocols.ProtocolClaude},
		{"gemini", protocols.ProtocolGemini},
		{"responses", protocols.ProtocolResponses},
		{"bogus", protocols.ProtocolOpenAI},
	}
	for _, tc := range cases {
		if got := TypeOf(tc.in); got != tc.want {
			t.Errorf("TypeOf(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestAllEnumeratesTheVocabularyInStableOrder guards the enumeration other
// registries are held against.
func TestAllEnumeratesTheVocabularyInStableOrder(t *testing.T) {
	want := []protocols.ProtocolID{
		protocols.ProtocolClaude, // "anthropic" sorts first
		protocols.ProtocolGemini,
		protocols.ProtocolOpenAI,
		protocols.ProtocolResponses,
	}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("All() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("All()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
