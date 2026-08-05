package gateway

import "testing"

func TestIsChatEndpointAllowlist(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/chat/completions", true},
		{"/v1/messages", true},
		{"/v1/responses", true},
		{"/v1beta/models/gemini-pro:generateContent", true},
		{"/v1beta/models/gemini-pro:streamGenerateContent", true},
		{"/v1beta/models/gemini-pro:countTokens", false},
		{"/v1beta/models/gemini-pro:embedContent", false},
		{"/v1/embeddings", false},
		{"/v1/completions", false},
	}
	for _, c := range cases {
		if got := IsChatEndpoint(c.path); got != c.want {
			t.Errorf("IsChatEndpoint(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
