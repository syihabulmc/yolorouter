package visionfallback

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/loopback"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/testutil"

	"gorm.io/gorm"
)

type fakeView struct {
	proto      protocols.ProtocolID
	path       string
	chat       bool
	fbModel    string
	fbPrompt   string
	credential string
	subCall    bool
}

func (v fakeView) IngressProtocol() protocols.ProtocolID { return v.proto }
func (v fakeView) IngressPath() string                   { return v.path }
func (v fakeView) IsChatEndpoint() bool                  { return v.chat }
func (v fakeView) RequestID() string                     { return "req-vf-test" }
func (v fakeView) VisionFallbackModel() string           { return v.fbModel }
func (v fakeView) VisionFallbackPrompt() string          { return v.fbPrompt }
func (v fakeView) AuthCredential() string                { return v.credential }
func (v fakeView) IsVisionFallbackSubCall() bool         { return v.subCall }

type fakeSink struct {
	records []fact.Record
}

func (s *fakeSink) Report(...fact.Fact)    {}
func (s *fakeSink) Note(rs ...fact.Record) { s.records = append(s.records, rs...) }
func (s *fakeSink) recordNames() (out []string) {
	for _, r := range s.records {
		out = append(out, r.RecordName())
	}
	return out
}

func seedModel(t *testing.T, db *gorm.DB, name string, imageInput *bool) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO models (name, management_status, supports_image_input, created_at, updated_at) VALUES (?, 1, ?, '2026-01-01', '2026-01-01')`,
		name, imageInput,
	).Error; err != nil {
		t.Fatalf("seed model %s: %v", name, err)
	}
}

func chatBodyWithImage(model string) []byte {
	return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what is in this picture"},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}]}]}`)
}

func boolPtr(v bool) *bool { return &v }

// An undeclared model (NULL) must be left entirely alone — missing metadata
// must never degrade a possibly vision-capable model.
func TestUndeclaredModelPassesThroughUntouched(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "mystery", nil)
	f := New(db, "http://localhost:0")
	body := chatBodyWithImage("mystery")
	sink := &fakeSink{}

	out, err := f.RewriteIngress(context.Background(), fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true, fbModel: "eyes"}, body, sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("undeclared model's body was modified:\n%s", out)
	}
	if len(sink.records) != 0 {
		t.Fatalf("undeclared model produced records: %v", sink.recordNames())
	}
}

// A model declared image-capable is equally untouched. The record assertion
// is load-bearing: an implementation that wrongly ENTERS the pipeline for a
// capable model can still return identical bytes when its describe attempt
// fails, and only the absence of records distinguishes "never touched it"
// from "touched it and fell back".
func TestDeclaredCapableModelPassesThroughUntouched(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "seer", boolPtr(true))
	f := New(db, "http://localhost:0")
	body := chatBodyWithImage("seer")
	sink := &fakeSink{}

	out, err := f.RewriteIngress(context.Background(), fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true, fbModel: "eyes"}, body, sink)
	if err != nil || string(out) != string(body) {
		t.Fatalf("capable model's body was modified (err=%v):\n%s", err, out)
	}
	if len(sink.records) != 0 {
		t.Fatalf("capable model produced records: %v", sink.recordNames())
	}
}

// Declared unable + no fallback model configured: images become placeholders
// (strip), and the fact trail says so.
func TestStripWhenNoFallbackModelConfigured(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))
	f := New(db, "http://localhost:0")
	sink := &fakeSink{}

	out, err := f.RewriteIngress(context.Background(), fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true}, chatBodyWithImage("blind"), sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if strings.Contains(string(out), "image_url") || strings.Contains(string(out), "example.com/cat.png") {
		t.Fatalf("image survived the strip:\n%s", out)
	}
	if !strings.Contains(string(out), strippedPlaceholder) {
		t.Fatalf("no placeholder in stripped body:\n%s", out)
	}
	if !strings.Contains(string(out), "what is in this picture") {
		t.Fatalf("text content lost in strip:\n%s", out)
	}
	names := sink.recordNames()
	if len(names) != 1 || names[0] != "vision_fallback_stripped" {
		t.Fatalf("records = %v, want [vision_fallback_stripped]", names)
	}
}

// Declared unable + fallback model configured: the image is described through
// the loopback endpoint and replaced with the description text.
func TestDescribeReplacesImageWithDescription(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))

	var gotAuth, gotInternal, gotParent string
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotInternal = r.Header.Get(loopback.HeaderInternal)
		gotParent = r.Header.Get(loopback.HeaderParent)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "eyes" {
			t.Errorf("describe call model = %q, want eyes", req.Model)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a cat on a keyboard"}}]}`))
	}))
	defer loop.Close()

	f := New(db, loop.URL)
	sink := &fakeSink{}
	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true, fbModel: "eyes", credential: "sk-caller"},
		chatBodyWithImage("blind"), sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !strings.Contains(string(out), "a cat on a keyboard") {
		t.Fatalf("description missing from rewritten body:\n%s", out)
	}
	if strings.Contains(string(out), "image_url") {
		t.Fatalf("image part survived the replace:\n%s", out)
	}
	if gotAuth != "Bearer sk-caller" {
		t.Fatalf("loopback auth = %q, want the caller's own credential", gotAuth)
	}
	if gotInternal != loopback.Token {
		t.Fatalf("loopback internal marker missing or wrong: %q", gotInternal)
	}
	if gotParent != "req-vf-test" {
		t.Fatalf("loopback parent id = %q", gotParent)
	}
	names := sink.recordNames()
	if len(names) != 1 || names[0] != "vision_fallback_applied" {
		t.Fatalf("records = %v, want [vision_fallback_applied]", names)
	}
}

// When every describe attempt fails, the caller's own bytes go through
// untouched — a transient describe outage must not silently strip content.
func TestAllDescribesFailedForwardsOriginal(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer loop.Close()

	f := New(db, loop.URL)
	body := chatBodyWithImage("blind")
	sink := &fakeSink{}
	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true, fbModel: "eyes"}, body, sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("failed describe must forward the original body, got:\n%s", out)
	}
	names := sink.recordNames()
	if len(names) != 1 || names[0] != "vision_fallback_skipped" {
		t.Fatalf("records = %v, want [vision_fallback_skipped]", names)
	}
}

// The recursion guard: a loopback self-call must never re-enter the
// capability, or one image request would fan out forever.
func TestAppliesRefusesSubCalls(t *testing.T) {
	f := New(nil, "")
	if f.Applies(fakeView{chat: true, subCall: true}) {
		t.Fatal("Applies must refuse a vision-fallback sub-call")
	}
	if !f.Applies(fakeView{chat: true}) {
		t.Fatal("Applies must accept a normal chat request")
	}
	if f.Applies(fakeView{chat: false}) {
		t.Fatal("Applies must refuse non-chat endpoints")
	}
}

// Claude ingress goes through the same IR walk: strip works on an Anthropic
// body without protocol-specific code in this package.
func TestStripWorksOnClaudeBody(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))
	f := New(db, "http://localhost:0")
	body := []byte(`{"model":"blind","max_tokens":100,"messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}},` +
		`{"type":"text","text":"describe"}]}]}`)

	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolClaude, path: "/v1/messages", chat: true}, body, &fakeSink{})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if strings.Contains(string(out), "base64") || strings.Contains(string(out), "aGk=") {
		t.Fatalf("claude image survived the strip:\n%s", out)
	}
	if !strings.Contains(string(out), strippedPlaceholder) {
		t.Fatalf("no placeholder in claude strip:\n%s", out)
	}
}

// The regression that motivated JSON patching over an IR round-trip: fields
// the IR does not model (sampling knobs, user tags, provider extras) must
// survive a strip byte-for-byte.
func TestStripPreservesUnmodeledFields(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))
	f := New(db, "http://localhost:0")
	body := []byte(`{"model":"blind","seed":123456789012345678,"logit_bias":{"50256":-100},"user":"tenant-7","messages":[{"role":"user","name":"alice","content":[` +
		`{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}]}]}`)

	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true}, body, &fakeSink{})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	for _, want := range []string{`"seed":123456789012345678`, `"logit_bias":{"50256":-100}`, `"user":"tenant-7"`, `"name":"alice"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("unmodeled field lost: want %s in\n%s", want, out)
		}
	}
	if !strings.Contains(string(out), strippedPlaceholder) {
		t.Fatalf("image not stripped:\n%s", out)
	}
}

// An image over the size limit is not a transient failure: it gets the
// over-limit placeholder instead of riding through to a model that cannot
// read it.
func TestOverLimitImageGetsPlaceholderNotForwarded(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))
	f := New(db, "http://localhost:0")
	huge := strings.Repeat("A", maxBase64Len+1)
	body := []byte(`{"model":"blind","messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + huge + `"}}]}]}`)
	sink := &fakeSink{}

	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true, fbModel: "eyes"}, body, sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if strings.Contains(string(out), huge[:64]) {
		t.Fatalf("over-limit image was forwarded to a declared-blind model")
	}
	if !strings.Contains(string(out), limitPlaceholder) {
		t.Fatalf("over-limit image did not get the limit placeholder:\n%.300s", out)
	}
	// No describe ran, so the record must not name the describe model —
	// this counts as a strip, not an application.
	names := sink.recordNames()
	if len(names) != 1 || names[0] != "vision_fallback_stripped" {
		t.Fatalf("records = %v, want [vision_fallback_stripped]", names)
	}
}

// A partial describe failure keeps the successes and placeholders the
// failure — one bad image must not degrade the whole request to strip or
// passthrough.
func TestPartialDescribeFailureMixesDescriptionAndPlaceholder(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "good.png") {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a good picture"}}]}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer loop.Close()

	f := New(db, loop.URL)
	body := []byte(`{"model":"blind","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"https://example.com/good.png"}},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/bad.png"}}]}]}`)
	sink := &fakeSink{}

	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true, fbModel: "eyes"}, body, sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !strings.Contains(string(out), "a good picture") {
		t.Fatalf("successful description missing:\n%s", out)
	}
	if !strings.Contains(string(out), failedPlaceholder) {
		t.Fatalf("failed image did not get the failure placeholder:\n%s", out)
	}
	if strings.Contains(string(out), "bad.png") {
		t.Fatalf("failed image part survived:\n%s", out)
	}
	names := sink.recordNames()
	if len(names) != 1 || names[0] != "vision_fallback_applied" {
		t.Fatalf("records = %v, want [vision_fallback_applied]", names)
	}
}

// Image number 11 is over the count limit: described up to the cap, the
// rest placeholdered — never forwarded.
func TestImageCountLimitCapsDescribes(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pic"}}]}`))
	}))
	defer loop.Close()

	parts := make([]string, 0, maxImages+1)
	for i := 0; i <= maxImages; i++ {
		parts = append(parts, `{"type":"image_url","image_url":{"url":"https://example.com/img-`+string(rune('a'+i))+`.png"}}`)
	}
	body := []byte(`{"model":"blind","messages":[{"role":"user","content":[` + strings.Join(parts, ",") + `]}]}`)

	out, err := New(db, loop.URL).RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true, fbModel: "eyes"}, body, &fakeSink{})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !strings.Contains(string(out), limitPlaceholder) {
		t.Fatalf("image beyond the count limit did not get the limit placeholder")
	}
	if got := strings.Count(string(out), "[Image description: pic]"); got != maxImages {
		t.Fatalf("described %d images, want exactly %d", got, maxImages)
	}
}

// Gemini file_data may carry any modality; only image/* is this
// capability's business — a video sent to an images-blind model must pass
// through untouched.
func TestGeminiNonImageFileDataUntouched(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind-gem", boolPtr(false))
	f := New(db, "http://localhost:0")
	body := []byte(`{"contents":[{"role":"user","parts":[` +
		`{"fileData":{"fileUri":"https://example.com/talk.mp4","mimeType":"video/mp4"}},` +
		`{"text":"summarize"}]}]}`)
	sink := &fakeSink{}

	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolGemini, path: "/v1beta/models/blind-gem:generateContent", chat: true, fbModel: "eyes"}, body, sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("non-image file data was modified:\n%s", out)
	}
	if len(sink.records) != 0 {
		t.Fatalf("non-image file data produced records: %v", sink.recordNames())
	}
}

// An inline data: URL on the chat endpoint counts against the size limit
// exactly like a Claude base64 source — leaving payloadLen at zero would
// let a near-request-cap image ride into the loopback despite the contract.
func TestChatDataURLCountsAgainstSizeLimit(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind", boolPtr(false))
	f := New(db, "http://localhost:0")
	huge := "data:image/png;base64," + strings.Repeat("A", maxBase64Len)
	body := []byte(`{"model":"blind","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + huge + `"}}]}]}`)
	sink := &fakeSink{}

	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolOpenAI, path: "/v1/chat/completions", chat: true, fbModel: "eyes"}, body, sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !strings.Contains(string(out), limitPlaceholder) {
		t.Fatalf("oversized data URL did not get the limit placeholder")
	}
	if strings.Contains(string(out), "base64,AAAA") {
		t.Fatalf("oversized data URL survived")
	}
}

// Responses ingress: an in-content input_image part AND a bare top-level
// input_image item both strip; the bare item becomes a role-bearing message
// so downstream decoders keep recognizing the entry, and unmodeled top-level
// fields survive.
func TestStripWorksOnResponsesBody(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedModel(t, db, "blind-r", boolPtr(false))
	f := New(db, "http://localhost:0")
	body := []byte(`{"model":"blind-r","store":true,"metadata":{"trace":"t-1"},"input":[` +
		`{"type":"input_image","image_url":"https://example.com/bare.png"},` +
		`{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/part.png"},{"type":"input_text","text":"look"}]}]}`)
	sink := &fakeSink{}

	out, err := f.RewriteIngress(context.Background(),
		fakeView{proto: protocols.ProtocolResponses, path: "/v1/responses", chat: true}, body, sink)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if strings.Contains(string(out), "bare.png") || strings.Contains(string(out), "part.png") {
		t.Fatalf("responses image survived the strip:\n%s", out)
	}
	if got := strings.Count(string(out), strippedPlaceholder); got != 2 {
		t.Fatalf("placeholder count = %d, want 2:\n%s", got, out)
	}
	// The bare top-level item must have become a role-bearing message.
	if !strings.Contains(string(out), `"role":"user","content":[{"type":"input_text"`) {
		t.Fatalf("bare input_image was not wrapped into a message item:\n%s", out)
	}
	for _, want := range []string{`"store":true`, `"trace":"t-1"`, `"text":"look"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("field lost in responses strip: want %s in\n%s", want, out)
		}
	}
	names := sink.recordNames()
	if len(names) != 1 || names[0] != "vision_fallback_stripped" {
		t.Fatalf("records = %v, want [vision_fallback_stripped]", names)
	}
}
