package compress

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/compress/compressors"
)

// panicCompressor is a test double that always panics inside Compress. It is
// used to exercise runCompress's defer-recover fail-open path: a panicking
// leaf compressor must never escape the pass — the original body is returned
// untouched with SkipReasonFailOpen.
type panicCompressor struct{}

func (*panicCompressor) Name() string { return "panic" }
func (*panicCompressor) Compress(context.Context, string) (string, error) {
	panic("intentional panic from panicCompressor")
}

func TestCompressClaudeShrinksToolResultKeepsRest(t *testing.T) {
	big := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n" + strings.Repeat("=== RUN   TestX\n--- PASS: TestX (0.00s)\n", 200) + "PASS\nok  \tpkg\t0.1s\n"
	body := []byte(`{"model":"claude","system":"SYS","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":` + mustJSONStr(big) + `}]}]}`)
	out, res := CompressClaude(context.Background(), body, DefaultOptions())
	if res.Skipped {
		t.Fatalf("expected compression, got skip=%v", res.SkipReason)
	}
	if len(out) >= len(body) {
		t.Fatal("output should be shorter than input")
	}
	// The frozen prefix (everything outside the live zone) must be preserved
	// byte-for-byte; only the live-zone text is rewritten.
	if !bytes.Contains(out, []byte(`"system":"SYS"`)) {
		t.Fatal("system prefix must be preserved byte-for-byte")
	}
	if res.EstimatedTokensSaved <= 0 {
		t.Fatal("expected positive token savings")
	}
}

func TestCompressClaudeNoLiveZoneIsIdentity(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`) // no tool_result
	out, res := CompressClaude(context.Background(), body, DefaultOptions())
	if !bytes.Equal(out, body) {
		t.Fatal("no-op must return bytes.Equal output")
	}
	if !res.Skipped {
		t.Fatal("expected Skipped=true")
	}
}

func TestCompressClaudeParseErrorIsIdentity(t *testing.T) {
	body := []byte(`{not json`)
	out, res := CompressClaude(context.Background(), body, DefaultOptions())
	if !bytes.Equal(out, body) || res.SkipReason != SkipReasonParseError {
		t.Fatal("invalid JSON must be returned verbatim with ParseError")
	}
}

func TestCompressSkipReasonNoLiveZone(t *testing.T) {
	// Claude live zone collects only tool_result blocks; a user message with
	// bare-string content has none, so locate returns an empty slice and the
	// true skip reason is NoLiveZone.
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	_, res := CompressClaude(context.Background(), body, DefaultOptions())
	if !res.Skipped || res.SkipReason != SkipReasonNoLiveZone {
		t.Fatalf("expected NoLiveZone, got skip=%v reason=%v", res.Skipped, res.SkipReason)
	}
}

func TestCompressSkipReasonNoMatchingCompressor(t *testing.T) {
	// A large prose block (no diff/gotest/grep/log anchors) is detected as
	// PlainText, for which compressorsFor returns nil, so the pass surfaces
	// a real coverage gap as NoMatchingCompressor.
	prose := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)
	body := []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":` + mustJSONStr(prose) + `}]}]}`)
	_, res := CompressClaude(context.Background(), body, DefaultOptions())
	if !res.Skipped || res.SkipReason != SkipReasonNoMatchingCompressor {
		t.Fatalf("expected NoMatchingCompressor, got skip=%v reason=%v", res.Skipped, res.SkipReason)
	}
}

func TestCompressSkipReasonNoEffectiveReplacement(t *testing.T) {
	// A block smaller than MinBlockBytes (512) is skipped by shouldAttempt
	// before content-type detection runs, so sawNoCompressor stays false and
	// the skip reason resolves to NoEffectiveReplacement rather than
	// NoMatchingCompressor.
	tiny := "=== RUN   TestA\n--- PASS: TestA (0.00s)\nPASS\n"
	body := []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":` + mustJSONStr(tiny) + `}]}]}`)
	_, res := CompressClaude(context.Background(), body, DefaultOptions())
	if !res.Skipped || res.SkipReason != SkipReasonNoEffectiveReplacement {
		t.Fatalf("expected NoEffectiveReplacement, got skip=%v reason=%v", res.Skipped, res.SkipReason)
	}
}

func TestCompressSkipReasonFailOpenOnPanic(t *testing.T) {
	// Swap the package-level build-compressor chain for a single panicking
	// compressor. runCompress's defer-recover must catch the panic and return
	// the ORIGINAL body untouched with SkipReasonFailOpen. This test is serial:
	// it mutates package state (buildCompressors) and must not run in parallel
	// with any other test that reads the same chain.
	saved := buildCompressors
	buildCompressors = []compressors.Compressor{&panicCompressor{}}
	t.Cleanup(func() { buildCompressors = saved })

	// Build-output anchors (=== RUN, --- PASS, ok) cause detectContentType to
	// return ContentBuildOutput, which routes to buildCompressors.
	big := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n" +
		strings.Repeat("=== RUN   TestX\n--- PASS: TestX (0.00s)\n", 200) +
		"PASS\nok  \tpkg\t0.1s\n"
	body := []byte(`{"messages":[{"role":"user","content":` + mustJSONStr(big) + `}]}`)

	out, res := CompressChat(context.Background(), body, DefaultOptions())
	if !bytes.Equal(out, body) {
		t.Fatal("panic must fail open: returned body must be bytes.Equal to original")
	}
	if !res.Skipped || res.SkipReason != SkipReasonFailOpen {
		t.Fatalf("expected Skipped=true SkipReason=FailOpen, got skip=%v reason=%v", res.Skipped, res.SkipReason)
	}
}

func TestCompressSkipReasonTimeout(t *testing.T) {
	// With opts.Timeout = 1ns, the context deadline elapses before the per-block
	// loop reaches its first iteration (json.Valid + locate + size-gate already
	// take microseconds). The select { case <-ctx.Done() } guard at the top of
	// each block iteration therefore fires immediately and returns the original
	// body with SkipReasonTimeout. 1ns is the smallest non-zero Duration; it is
	// reliable here because the timeout is checked at the head of each block
	// iteration rather than mid-compress, so any elapsed budget is sufficient.
	big := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n" +
		strings.Repeat("=== RUN   TestX\n--- PASS: TestX (0.00s)\n", 200) +
		"PASS\nok  \tpkg\t0.1s\n"
	body := []byte(`{"messages":[{"role":"user","content":` + mustJSONStr(big) + `}]}`)

	opts := DefaultOptions()
	opts.Timeout = 1 * time.Nanosecond
	out, res := CompressChat(context.Background(), body, opts)
	if !bytes.Equal(out, body) {
		t.Fatal("timeout must return original body untouched")
	}
	if !res.Skipped || res.SkipReason != SkipReasonTimeout {
		t.Fatalf("expected Skipped=true SkipReason=Timeout, got skip=%v reason=%v", res.Skipped, res.SkipReason)
	}
}

func mustJSONStr(s string) string { b, _ := jsonMarshal(s); return string(b) }
