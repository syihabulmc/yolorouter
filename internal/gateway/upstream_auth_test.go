package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// credentialExpectations records, per egress protocol, where its provider reads
// the key from and which other headers it refuses the request without.
//
// Cross-checked against codecRegistry in both directions rather than used as
// the test's own list of cases. A protocol added to the registry and forgotten
// here fails, which is the point: a hand-written list of cases would simply not
// mention the new protocol and stay green.
var credentialExpectations = map[protocols.ProtocolID]struct {
	header string
	// alsoPresent names headers whose exact value is the protocol's own
	// business but whose absence the provider rejects.
	alsoPresent []string
}{
	protocols.ProtocolOpenAI:    {header: "Authorization"},
	protocols.ProtocolClaude:    {header: "x-api-key", alsoPresent: []string{"anthropic-version"}},
	protocols.ProtocolGemini:    {header: "x-goog-api-key"},
	protocols.ProtocolResponses: {header: "Authorization"},
}

// TestEachEgressProtocolCarriesItsOwnCredentials pins the step that happens
// after the request body is built and cannot be seen in it.
//
// The key is attached per attempt, not per candidate, because a candidate can
// be retried on a second key when the first turns out to be dead. Each protocol
// expects it somewhere different, and a key put in the wrong header reads as an
// unauthenticated request: the provider answers 401, the 401 handler marks the
// key verification-failed, and a working credential is retired for a mistake on
// our side.
//
// The failure is quiet by construction — see the fallback pinned below.
func TestEachEgressProtocolCarriesItsOwnCredentials(t *testing.T) {
	const key = "test-key-123"

	for proto := range codecRegistry {
		t.Run(string(proto), func(t *testing.T) {
			want, known := credentialExpectations[proto]
			if !known {
				t.Fatalf("%s is a registered egress protocol with nothing recorded about its credentials. "+
					"Say where its provider reads the key from: leaving it out does not skip the check, "+
					"it ships the key wherever the OpenAI fallback puts it.", proto)
			}

			req, err := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/x", nil)
			if err != nil {
				t.Fatalf("http.NewRequest: %v", err)
			}
			codecsFor(proto).RequestEncoder.SetupRequest(req, key)

			if got := req.Header.Get(want.header); !strings.Contains(got, key) {
				t.Errorf("%s = %q, want it to carry the key", want.header, got)
			}
			for _, header := range want.alsoPresent {
				if req.Header.Get(header) == "" {
					t.Errorf("%s is missing; this provider refuses a request without it", header)
				}
			}
			if req.Header.Get("Content-Type") == "" {
				t.Error("no content type; the provider is left to guess how to parse the body")
			}

			// A credential in a second place is as bad as one in the wrong
			// place: whichever header the provider does not read, it logs.
			canonical := http.CanonicalHeaderKey(want.header)
			for name, values := range req.Header {
				if http.CanonicalHeaderKey(name) == canonical {
					continue
				}
				for _, v := range values {
					if strings.Contains(v, key) {
						t.Errorf("the key also went out in %s: %q", name, v)
					}
				}
			}
		})
	}

	// The other direction: an expectation left behind for a protocol nobody
	// routes to any more reads as coverage that is not there.
	for proto := range credentialExpectations {
		if _, ok := codecRegistry[proto]; !ok {
			t.Errorf("%s has credentials recorded but is not a registered egress protocol", proto)
		}
	}
}

// TestAnUnregisteredProtocolShipsTheKeyAsABearerToken pins what makes the table
// above load-bearing rather than decorative.
//
// codecsFor answers for a protocol it has never heard of, and what it answers
// with is the OpenAI codecs. That is a deliberate default, not a bug — but it
// means the failure mode for a forgotten registry entry is a valid-looking
// request with the key in the wrong header, which nothing in the type system
// can catch and no provider reports as anything but 401.
func TestAnUnregisteredProtocolShipsTheKeyAsABearerToken(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/x", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	codecsFor(protocols.ProtocolID("no-such-protocol")).RequestEncoder.SetupRequest(req, "k")

	if got := req.Header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("Authorization = %q, want the OpenAI fallback's bearer token — "+
			"if this changed, the table above is guarding against the wrong hazard", got)
	}
}
