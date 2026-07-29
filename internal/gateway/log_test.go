package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

func TestComputeCost(t *testing.T) {
	// Candidate prices are CNY per million tokens. Cost is
	// stored as integer micros (CNY × 1e6, i.e. 6-decimal precision).
	// 1M input @ 1.0 + 0.5M output @ 2.0 = 1.0 + 1.0 = 2.0 CNY = 2_000_000 micros.
	cand := &model.ModelCandidate{InputPrice: 1.0, OutputPrice: 2.0}
	usage := &Usage{PromptTokens: 1_000_000, CompletionTokens: 500_000}
	cost := computeCost(cand, usage, 0)
	if !cost.Known {
		t.Fatal("expected cost to be known when usage + candidate present")
	}
	if cost.CostMicros != 2_000_000 {
		t.Fatalf("cost = %d micros, want 2_000_000", cost.CostMicros)
	}
}

func TestComputeCostCacheEconomics(t *testing.T) {
	// input @ 3.0/M, cache read @ 0.3/M (cheaper), cache write @ 3.75/M (a
	// premium over input). 1M cache-read tokens save (3.0−0.3)=2.7 CNY;
	// 1M cache-write tokens cost an extra (3.75−3.0)=0.75 CNY.
	readPrice, writePrice := 0.3, 3.75
	cand := &model.ModelCandidate{
		InputPrice:      3.0,
		OutputPrice:     6.0,
		CacheReadPrice:  &readPrice,
		CacheWritePrice: &writePrice,
	}
	usage := &Usage{PromptTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000}
	cost := computeCost(cand, usage, 0)
	if cost.CacheReadSavedMicros != 2_700_000 {
		t.Errorf("cache read saved = %d, want 2_700_000", cost.CacheReadSavedMicros)
	}
	if cost.CacheWriteExtraMicros != 750_000 {
		t.Errorf("cache write extra = %d, want 750_000", cost.CacheWriteExtraMicros)
	}
}

func TestComputeCostNoCachePriceHasNoSavings(t *testing.T) {
	// Without configured cache prices, cache tokens bill at the input price, so
	// there is neither a read saving nor a write premium.
	cand := &model.ModelCandidate{InputPrice: 2.0, OutputPrice: 4.0}
	usage := &Usage{PromptTokens: 1_000_000, CacheReadTokens: 500_000, CacheWriteTokens: 500_000}
	cost := computeCost(cand, usage, 0)
	if cost.CacheReadSavedMicros != 0 || cost.CacheWriteExtraMicros != 0 {
		t.Errorf("expected 0/0 savings without cache prices, got read=%d write=%d",
			cost.CacheReadSavedMicros, cost.CacheWriteExtraMicros)
	}
}

func TestComputeCostRoundsToMicro(t *testing.T) {
	// Micros are the smallest stored unit (CNY × 1e6). 1 token @ 1.5/M =
	// 0.0000015 CNY = 1.5 micros -> rounds to 2 micros.
	cand := &model.ModelCandidate{InputPrice: 1.5, OutputPrice: 0}
	usage := &Usage{PromptTokens: 1, CompletionTokens: 0}
	cost := computeCost(cand, usage, 0)
	if !cost.Known || cost.CostMicros != 2 {
		t.Fatalf("expected known 2 micros, got %d (known=%v)", cost.CostMicros, cost.Known)
	}
}

func TestComputeCostMissingUsageIsUnknown(t *testing.T) {
	// Missing usage must be "unknown", never 0 cost.
	cand := &model.ModelCandidate{InputPrice: 1.0, OutputPrice: 1.0}
	if cost := computeCost(cand, nil, 0); cost.Known || cost.CostMicros != 0 {
		t.Fatalf("expected unknown/0 for nil usage, got %d (known=%v)", cost.CostMicros, cost.Known)
	}
}

func TestComputeCostMissingCandidateIsUnknown(t *testing.T) {
	usage := &Usage{PromptTokens: 100, CompletionTokens: 100}
	if cost := computeCost(nil, usage, 0); cost.Known || cost.CostMicros != 0 {
		t.Fatalf("expected unknown/0 for nil candidate, got %d (known=%v)", cost.CostMicros, cost.Known)
	}
}

// TestFinalizeWritesBodyRow: finalize
// persists the four body fields (stored verbatim; v0.1 does not scrub body
// content) into request_log_bodies, keyed by request_id, alongside the
// request_logs row.
func TestFinalizeWritesBodyRow(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)

	rc := &RelayContext{
		RequestID:            "req-body-1",
		APIKeyID:             apiKey.ID,
		RequestHeaders:       []byte(`{"User-Agent":["curl/8.0"]}`),
		RequestBody:          []byte(`{"model":"gpt-4o"}`),
		UpstreamRequestBody:  []byte(`{"model":"gpt-4o-real"}`),
		ResponseBody:         []byte(`{"model":"gpt-4o","choices":[]}`),
		UpstreamResponseBody: []byte(`{"model":"gpt-4o-real","choices":[]}`),
	}

	svc.finalize(rc, 200, "", time.Now())

	var logCount int64
	db.Model(&model.RequestLog{}).Count(&logCount)
	if logCount != 1 {
		t.Fatalf("expected 1 request_logs row, got %d", logCount)
	}

	body, err := repository.GetRequestLogBodyByRequestID(db, "req-body-1")
	if err != nil {
		t.Fatalf("GetRequestLogBodyByRequestID: %v", err)
	}
	if body == nil {
		t.Fatal("expected a request_log_bodies row, got none")
	}
	if body.RequestHeaders != `{"User-Agent":["curl/8.0"]}` {
		t.Errorf("RequestHeaders = %q, want the captured (masked) headers", body.RequestHeaders)
	}
	if body.RequestBody != `{"model":"gpt-4o"}` {
		t.Errorf("RequestBody = %q, want caller's original request", body.RequestBody)
	}
	if body.UpstreamRequestBody != `{"model":"gpt-4o-real"}` {
		t.Errorf("UpstreamRequestBody = %q, want rewritten upstream request", body.UpstreamRequestBody)
	}
	if body.ResponseBody != `{"model":"gpt-4o","choices":[]}` {
		t.Errorf("ResponseBody = %q, want caller-facing response", body.ResponseBody)
	}
	if body.UpstreamResponseBody != `{"model":"gpt-4o-real","choices":[]}` {
		t.Errorf("UpstreamResponseBody = %q, want raw upstream response", body.UpstreamResponseBody)
	}
	if body.StreamBodyPath != "" {
		t.Errorf("StreamBodyPath = %q, want empty for a non-stream request", body.StreamBodyPath)
	}
}

// TestFinalizeBodyWriteFailureDoesNotRollbackBilling: the
// request_log_bodies write is best-effort — a failure there must not roll
// back or block the request_logs row (billing/audit trail), which is written
// first and is authoritative.
func TestFinalizeBodyWriteFailureDoesNotRollbackBilling(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)

	// Force UpsertRequestLogBody to fail without touching request_logs, so
	// the assertion below proves the body write's failure never rolled back
	// (or panicked around) the earlier billing/log write.
	if err := db.Exec("DROP TABLE request_log_bodies").Error; err != nil {
		t.Fatalf("drop request_log_bodies: %v", err)
	}

	rc := &RelayContext{RequestID: "req-body-fail-1", APIKeyID: apiKey.ID}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("finalize panicked on request_log_bodies write failure: %v", r)
		}
	}()
	svc.finalize(rc, 200, "", time.Now())

	var logCount int64
	db.Model(&model.RequestLog{}).Count(&logCount)
	if logCount != 1 {
		t.Fatalf("expected request_logs row despite body-write failure, got count=%d", logCount)
	}
}

func TestGenerateRequestIDUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := generateRequestID()
		if len(id) != 16 {
			t.Fatalf("id length = %d, want 16 (id=%q)", len(id), id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}

// TestComputeCostCompressSavings verifies the ESTIMATED compress saving is
// priced at the candidate's input rate. 1500 tokens saved @ 2.5 CNY/M =
// 1500 * 2.5 = 3750 micros (the /1e6 from per-M pricing cancels the ×1e6
// to micros). Reported only — CostMicros itself is unaffected.
func TestComputeCostCompressSavings(t *testing.T) {
	cand := &model.ModelCandidate{InputPrice: 2.5, OutputPrice: 5.0}
	usage := &Usage{PromptTokens: 100, CompletionTokens: 50}
	cost := computeCost(cand, usage, 1500)
	if !cost.Known {
		t.Fatal("expected cost to be known when usage + candidate present")
	}
	if cost.CompressCostSavedMicros != 3750 {
		t.Errorf("compress cost saved = %d, want 3750 micros", cost.CompressCostSavedMicros)
	}
	// Sanity: the billed CostMicros is unaffected by the compress savings line.
	bare := computeCost(cand, usage, 0)
	if cost.CostMicros != bare.CostMicros {
		t.Errorf("compress savings leaked into CostMicros: with=%d without=%d",
			cost.CostMicros, bare.CostMicros)
	}
}

// TestComputeCostCompressSavingsRoundingFractional: a fractional input price
// must round to the nearest micro (matching the CostMicros rounding policy).
// 1 token @ 1.5/M = 1.5 micros -> rounds to 2.
func TestComputeCostCompressSavingsRoundingFractional(t *testing.T) {
	cand := &model.ModelCandidate{InputPrice: 1.5, OutputPrice: 0}
	usage := &Usage{PromptTokens: 1, CompletionTokens: 0}
	cost := computeCost(cand, usage, 1)
	if cost.CompressCostSavedMicros != 2 {
		t.Errorf("compress cost saved = %d, want 2 micros after rounding", cost.CompressCostSavedMicros)
	}
}

// TestComputeCostCompressSavingsUnknownIsZero: when usage is nil (pricing/
// usage unknown), cost_saved must be 0 even if tokens_saved > 0 — matching
// the CostKnown=false semantics so an unknown row never reports a saving.
func TestComputeCostCompressSavingsUnknownIsZero(t *testing.T) {
	cand := &model.ModelCandidate{InputPrice: 2.0, OutputPrice: 4.0}
	if cost := computeCost(cand, nil, 1000); cost.Known || cost.CompressCostSavedMicros != 0 {
		t.Fatalf("expected unknown/0 compress savings for nil usage, got %d (known=%v)",
			cost.CompressCostSavedMicros, cost.Known)
	}
	if cost := computeCost(nil, &Usage{PromptTokens: 1}, 1000); cost.Known || cost.CompressCostSavedMicros != 0 {
		t.Fatalf("expected unknown/0 compress savings for nil candidate, got %d (known=%v)",
			cost.CompressCostSavedMicros, cost.Known)
	}
}

// TestFinalizeWritesCompressColumns: when compression ran, the four compress
// columns on request_logs and compressed_request_body on request_log_bodies
// are populated. Cost_saved is the tokens-saved × input-price math verified
// in TestComputeCostCompressSavings; here we verify it persisted end-to-end.
func TestFinalizeWritesCompressColumns(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)

	// Cand InputPrice 2.0/M, tokensSaved 1500 -> 1500 * 2.0 = 3000 micros.
	// rc.Candidate is only read in-memory by computeCost (InputPrice), never
	// persisted by finalize — so an unpersisted in-memory candidate is enough.
	cand := &model.ModelCandidate{
		InputPrice: 2.0, OutputPrice: 4.0, MaxOutput: 128,
		SupportsStreaming: boolPtr(true), ManagementStatus: model.ModelCandidateStatusEnabled,
		VerificationStatus: model.ModelVerificationStatusPassed,
	}
	rc := &RelayContext{
		RequestID:                    "req-compress-1",
		APIKeyID:                     apiKey.ID,
		Candidate:                    cand,
		Usage:                        &Usage{PromptTokens: 100, CompletionTokens: 50},
		CompressEstimatedTokensSaved: 1500,
		CompressorsApplied:           []string{"whitespace", "whitespace", "contractions"},
		RequestBodyCompressed:        []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		// One successful attempt — the request reached upstream, so the
		// compress savings are real (not phantom) and must be persisted.
		Attempts: []AttemptRecord{{
			CandidateID: cand.ID, ProviderID: 1, ProviderName: "test",
			Outcome: AttemptSuccess, StatusCode: 200,
		}},
	}
	svc.finalize(rc, 200, "", time.Now())

	row, err := repository.GetRequestLogByRequestID(db, "req-compress-1")
	if err != nil || row == nil {
		t.Fatalf("GetRequestLogByRequestID: %v (row=%v)", err, row)
	}
	if row.CompressEstimatedTokensSaved != 1500 {
		t.Errorf("CompressEstimatedTokensSaved = %d, want 1500", row.CompressEstimatedTokensSaved)
	}
	if row.CompressEstimatedCostSavedMicros != 3000 {
		t.Errorf("CompressEstimatedCostSavedMicros = %d, want 3000", row.CompressEstimatedCostSavedMicros)
	}
	if row.CompressSkipReason != "" {
		t.Errorf("CompressSkipReason = %q, want empty (compression ran)", row.CompressSkipReason)
	}
	// Per-block occurrences preserved: two "whitespace" entries are both kept
	// so downstream stats can count how many times each compressor fired.
	if row.CompressorsApplied != "whitespace,whitespace,contractions" {
		t.Errorf("CompressorsApplied = %q, want %q", row.CompressorsApplied, "whitespace,whitespace,contractions")
	}
	if !row.CostKnown {
		t.Errorf("CostKnown = false, want true (usage + candidate present)")
	}

	body, err := repository.GetRequestLogBodyByRequestID(db, "req-compress-1")
	if err != nil || body == nil {
		t.Fatalf("GetRequestLogBodyByRequestID: %v (body=%v)", err, body)
	}
	if body.CompressedRequestBody != `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}` {
		t.Errorf("CompressedRequestBody = %q, want the post-compression bytes", body.CompressedRequestBody)
	}
}

// TestFinalizeWritesCompressSkippedColumns: when compression was enabled but
// skipped, skip_reason is populated and the savings/body fields are zero/empty.
func TestFinalizeWritesCompressSkippedColumns(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)

	rc := &RelayContext{
		RequestID:          "req-compress-skip-1",
		APIKeyID:           apiKey.ID,
		CompressSkipReason: "too_small",
	}
	svc.finalize(rc, 200, "", time.Now())

	row, err := repository.GetRequestLogByRequestID(db, "req-compress-skip-1")
	if err != nil || row == nil {
		t.Fatalf("GetRequestLogByRequestID: %v (row=%v)", err, row)
	}
	if row.CompressEstimatedTokensSaved != 0 {
		t.Errorf("CompressEstimatedTokensSaved = %d, want 0 (skipped)", row.CompressEstimatedTokensSaved)
	}
	if row.CompressEstimatedCostSavedMicros != 0 {
		t.Errorf("CompressEstimatedCostSavedMicros = %d, want 0 (skipped)", row.CompressEstimatedCostSavedMicros)
	}
	if row.CompressSkipReason != "too_small" {
		t.Errorf("CompressSkipReason = %q, want %q", row.CompressSkipReason, "too_small")
	}
	if row.CompressorsApplied != "" {
		t.Errorf("CompressorsApplied = %q, want empty (skipped)", row.CompressorsApplied)
	}

	body, err := repository.GetRequestLogBodyByRequestID(db, "req-compress-skip-1")
	if err != nil || body == nil {
		t.Fatalf("GetRequestLogBodyByRequestID: %v (body=%v)", err, body)
	}
	if body.CompressedRequestBody != "" {
		t.Errorf("CompressedRequestBody = %q, want empty (skipped)", body.CompressedRequestBody)
	}
}

// TestFinalizeWritesCompressColumnsUncompressed: when compression never ran
// (CompressEnabled false — fields all zero-value), the four compress columns
// and compressed_request_body stay at their zero/empty defaults.
func TestFinalizeWritesCompressColumnsUncompressed(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)

	rc := &RelayContext{RequestID: "req-nocompress-1", APIKeyID: apiKey.ID}
	svc.finalize(rc, 200, "", time.Now())

	row, err := repository.GetRequestLogByRequestID(db, "req-nocompress-1")
	if err != nil || row == nil {
		t.Fatalf("GetRequestLogByRequestID: %v (row=%v)", err, row)
	}
	if row.CompressEstimatedTokensSaved != 0 || row.CompressEstimatedCostSavedMicros != 0 {
		t.Errorf("compress savings not zero for an uncompressed request: tokens=%d cost=%d",
			row.CompressEstimatedTokensSaved, row.CompressEstimatedCostSavedMicros)
	}
	if row.CompressSkipReason != "" || row.CompressorsApplied != "" {
		t.Errorf("compress reason/list not empty for an uncompressed request: reason=%q list=%q",
			row.CompressSkipReason, row.CompressorsApplied)
	}

	body, err := repository.GetRequestLogBodyByRequestID(db, "req-nocompress-1")
	if err != nil || body == nil {
		t.Fatalf("GetRequestLogBodyByRequestID: %v (body=%v)", err, body)
	}
	if body.CompressedRequestBody != "" {
		t.Errorf("CompressedRequestBody = %q, want empty for uncompressed request", body.CompressedRequestBody)
	}
}

// TestFinalizeCompressCostSavedZeroWhenPricingUnknown: a request that saved
// tokens but had no usage (early failure before any attempt) must persist
// cost_saved=0 even though tokens_saved may be non-zero. This is the case the
// dashboard cares about: never report a saving on a row whose cost is unknown.
func TestFinalizeCompressCostSavedZeroWhenPricingUnknown(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)

	rc := &RelayContext{
		RequestID:                    "req-unknown-price-1",
		APIKeyID:                     apiKey.ID,
		CompressEstimatedTokensSaved: 5000, // large, but no usage to price it
		CompressorsApplied:           []string{"whitespace"},
		RequestBodyCompressed:        []byte(`{"model":"gpt-4o"}`),
		// Usage and Candidate are nil — pricing is unknown, and crucially
		// Attempts is empty (pre-relay rejection).
	}
	svc.finalize(rc, http.StatusInternalServerError, "no_candidate", time.Now())

	row, err := repository.GetRequestLogByRequestID(db, "req-unknown-price-1")
	if err != nil || row == nil {
		t.Fatalf("GetRequestLogByRequestID: %v (row=%v)", err, row)
	}
	if row.CostKnown {
		t.Errorf("CostKnown = true, want false (no usage to price)")
	}
	if row.CompressEstimatedCostSavedMicros != 0 {
		t.Errorf("CompressEstimatedCostSavedMicros = %d, want 0 when pricing is unknown",
			row.CompressEstimatedCostSavedMicros)
	}
	// tokens_saved is zeroed too: the request was rejected pre-relay so no
	// upstream call was made — persisting savings would inflate the compress
	// rate / savings metrics.
	if row.CompressEstimatedTokensSaved != 0 {
		t.Errorf("CompressEstimatedTokensSaved = %d, want 0 (pre-relay rejection)",
			row.CompressEstimatedTokensSaved)
	}
	if row.CompressorsApplied != "" {
		t.Errorf("CompressorsApplied = %q, want empty (pre-relay rejection)",
			row.CompressorsApplied)
	}
}

// TestFinalizeCompressPhantomSavingsZeroedOnPreRelayRejection: compression
// modified the body and produced savings, but the request was then rejected
// before any upstream attempt (no routable candidate, model disabled, etc.).
// The compress savings fields MUST be zeroed so the stats endpoint does not
// count phantom savings. CompressSkipReason is kept (audit-only). The
// compressed body on request_log_bodies is still persisted (for debugging).
func TestFinalizeCompressPhantomSavingsZeroedOnPreRelayRejection(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)

	rc := &RelayContext{
		RequestID:                    "req-phantom-1",
		APIKeyID:                     apiKey.ID,
		CompressEstimatedTokensSaved: 1500,
		CompressorsApplied:           []string{"whitespace", "log"},
		RequestBodyCompressed:        []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		// No Attempts — simulates a pre-relay rejection (e.g. no routable
		// candidate after compression already ran on the caller body).
	}
	// 503 = no routable candidate, the most common post-compress pre-relay path.
	svc.finalize(rc, http.StatusServiceUnavailable, "no_enabled_candidate", time.Now())

	row, err := repository.GetRequestLogByRequestID(db, "req-phantom-1")
	if err != nil || row == nil {
		t.Fatalf("GetRequestLogByRequestID: %v (row=%v)", err, row)
	}
	if row.CompressEstimatedTokensSaved != 0 {
		t.Errorf("CompressEstimatedTokensSaved = %d, want 0 (phantom savings zeroed)",
			row.CompressEstimatedTokensSaved)
	}
	if row.CompressEstimatedCostSavedMicros != 0 {
		t.Errorf("CompressEstimatedCostSavedMicros = %d, want 0 (phantom savings zeroed)",
			row.CompressEstimatedCostSavedMicros)
	}
	if row.CompressorsApplied != "" {
		t.Errorf("CompressorsApplied = %q, want empty (phantom savings zeroed)",
			row.CompressorsApplied)
	}
	// CompressSkipReason stays as-is — it's audit-only.
	// (empty here because compression ran, but the zeroing logic does NOT touch it)

	// The compressed body is still persisted on request_log_bodies for debugging.
	body, err := repository.GetRequestLogBodyByRequestID(db, "req-phantom-1")
	if err != nil || body == nil {
		t.Fatalf("GetRequestLogBodyByRequestID: %v (body=%v)", err, body)
	}
	if body.CompressedRequestBody == "" {
		t.Errorf("CompressedRequestBody should still be persisted for debugging even on pre-relay rejection")
	}
}

// TestJoinCompressors covers the comma-join helper directly: per-block
// occurrences are preserved (NOT deduped) so stats can count how many times
// each compressor fired, order is preserved, and empty input yields "".
func TestJoinCompressors(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"nil", nil, ""},
		{"empty", []string{}, ""},
		{"single", []string{"whitespace"}, "whitespace"},
		{"distinct", []string{"whitespace", "contractions"}, "whitespace,contractions"},
		{"duplicates preserved", []string{"whitespace", "contractions", "whitespace", "whitespace"}, "whitespace,contractions,whitespace,whitespace"},
		{"blanks filtered", []string{"", "whitespace", "", ""}, "whitespace"},
		{"repeats counted", []string{"log", "log", "diff"}, "log,log,diff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinCompressors(tc.in); got != tc.want {
				t.Errorf("joinCompressors(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNetPromptTokens(t *testing.T) {
	cases := []struct {
		name  string
		usage *Usage
		want  int
	}{
		{"nil usage", nil, 0},
		{
			// Anthropic: input_tokens is already net; flag false -> unchanged.
			"claude net input passes through",
			&Usage{PromptTokens: 237, CacheReadTokens: 17152, CacheIncludedInPrompt: false},
			237,
		},
		{
			// OpenAI: prompt_tokens includes cache; flag true -> subtract cache.
			"openai gross input has cache subtracted",
			&Usage{PromptTokens: 17389, CacheReadTokens: 17152, CacheIncludedInPrompt: true},
			237, // 17389 - 17152
		},
		{
			// Cache WRITE sits outside the prompt total under every protocol, so
			// it is never subtracted — only the read portion is. Deducting it
			// here would understate the input line of the bill.
			"cache write is not subtracted",
			&Usage{PromptTokens: 1000, CacheReadTokens: 600, CacheWriteTokens: 300, CacheIncludedInPrompt: true},
			400, // 1000 - 600; the 300 written tokens were never in prompt_tokens
		},
		{
			// Anthropic reports a net input_tokens alongside a cache write, so
			// neither count is subtracted.
			"claude net input with cache write passes through",
			&Usage{PromptTokens: 237, CacheWriteTokens: 4096, CacheIncludedInPrompt: false},
			237,
		},
		{
			// DeepSeek splits prompt_tokens into hit + miss; the hit half decodes
			// as cache read, leaving the miss half as net input.
			"deepseek hit subtracted leaving miss",
			&Usage{PromptTokens: 1000, CacheReadTokens: 600, CacheIncludedInPrompt: true},
			400, // the prompt_cache_miss_tokens half
		},
		{
			// Defensive: upstream reporting cache > prompt floors at 0.
			"cache exceeding prompt floors at zero",
			&Usage{PromptTokens: 100, CacheReadTokens: 500, CacheIncludedInPrompt: true},
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := netPromptTokens(tc.usage); got != tc.want {
				t.Errorf("netPromptTokens(%+v) = %d, want %d", tc.usage, got, tc.want)
			}
		})
	}
}

// TestComputeCostRejectsIncoherentUsage: token counts come off the wire from
// third-party upstreams, so a buggy or hostile provider can report negatives or
// a cache read larger than the prompt it was read from. Both must be treated as
// unknown usage — Known=false keeps the cost off the bill and, because finalize
// gates budget accumulation on Known, off the API key's budget too.
func TestComputeCostRejectsIncoherentUsage(t *testing.T) {
	readPrice := 0.02
	cand := &model.ModelCandidate{InputPrice: 1.0, OutputPrice: 2.0, CacheReadPrice: &readPrice}

	cases := []struct {
		name  string
		usage *Usage
	}{
		{
			// Would have billed 500 cache reads on a 100-token prompt.
			"cache read exceeds inclusive prompt",
			&Usage{PromptTokens: 100, CompletionTokens: 10, CacheReadTokens: 500, CacheIncludedInPrompt: true},
		},
		{
			// A negative cache count inflates net input AND produces a negative
			// cache line item, so the total can come out below zero.
			"negative cache read",
			&Usage{PromptTokens: 100, CompletionTokens: 10, CacheReadTokens: -1000, CacheIncludedInPrompt: true},
		},
		{"negative prompt", &Usage{PromptTokens: -5, CompletionTokens: 10}},
		{"negative completion", &Usage{PromptTokens: 100, CompletionTokens: -10}},
		{"negative cache write", &Usage{PromptTokens: 100, CompletionTokens: 10, CacheWriteTokens: -1}},
		{"negative total", &Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: -1}},
		{
			// An inflated completion count is otherwise indistinguishable from a
			// long generation; the upstream's own total is what contradicts it.
			"parts exceed stated total",
			&Usage{PromptTokens: 100, CompletionTokens: 1_000_000, TotalTokens: 110},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeCost(cand, tc.usage, 0)
			if got.Known {
				t.Errorf("Known = true, want false for incoherent usage %+v", tc.usage)
			}
			if got.CostMicros != 0 {
				t.Errorf("CostMicros = %d, want 0", got.CostMicros)
			}
		})
	}

	// A cache read equal to the whole prompt is legitimate (a fully cached
	// prompt) and must still bill.
	full := &Usage{PromptTokens: 100, CompletionTokens: 10, CacheReadTokens: 100, CacheIncludedInPrompt: true}
	if got := computeCost(cand, full, 0); !got.Known {
		t.Error("a fully-cached prompt must remain billable")
	}
	// Anthropic reports an independent net input, so a cache read larger than
	// it is normal rather than incoherent.
	netInput := &Usage{PromptTokens: 237, CompletionTokens: 10, CacheReadTokens: 17152, CacheIncludedInPrompt: false}
	if got := computeCost(cand, netInput, 0); !got.Known {
		t.Error("cache read may exceed a net (non-inclusive) input")
	}
	// An upstream that omits total_tokens leaves it at 0; the bound only
	// applies when a total was actually stated.
	noTotal := &Usage{PromptTokens: 100, CompletionTokens: 10}
	if got := computeCost(cand, noTotal, 0); !got.Known {
		t.Error("a missing total must not make usage incoherent")
	}
	// Exactly-equal parts and total is the normal OpenAI shape.
	exact := &Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}
	if got := computeCost(cand, exact, 0); !got.Known {
		t.Error("prompt+completion == total is the normal case")
	}
}

// TestComputeCostGuardsMicrosOverflow: float64→int64 conversion is undefined
// out of range, so an absurd unit price against a huge-but-coherent token count
// must yield unknown rather than an arbitrary budget charge.
func TestComputeCostGuardsMicrosOverflow(t *testing.T) {
	cand := &model.ModelCandidate{InputPrice: 1e12, OutputPrice: 1e12}
	usage := &Usage{PromptTokens: 1_000_000_000, CompletionTokens: 1_000_000_000, TotalTokens: 2_000_000_000}
	got := computeCost(cand, usage, 0)
	if got.Known {
		t.Errorf("Known = true, want false when micros overflows int64 (got %d)", got.CostMicros)
	}
}

func TestComputeCostUsesNetInputAcrossProtocols(t *testing.T) {
	// A converting upstream (Anthropic ingress) reports net input=237 with
	// cache_read=17152 (flag false); an OpenAI upstream reports the same
	// request as gross prompt=17389 with cache_read=17152 (flag true). Both
	// must bill identically: 237 net @ input price + 17152 @ cache_read price.
	readPrice := 0.02
	cand := &model.ModelCandidate{InputPrice: 1.0, OutputPrice: 2.0, CacheReadPrice: &readPrice}

	anthropic := &Usage{PromptTokens: 237, CompletionTokens: 10, CacheReadTokens: 17152, CacheIncludedInPrompt: false}
	openai := &Usage{PromptTokens: 17389, CompletionTokens: 10, CacheReadTokens: 17152, CacheIncludedInPrompt: true}

	// 237×1.0 + 17152×0.02 + 10×2.0 = 237 + 343.04 + 20 = 600.04 -> 600 micros.
	const wantMicros = 600
	if c := computeCost(cand, anthropic, 0); c.CostMicros != wantMicros {
		t.Errorf("anthropic cost = %d micros, want %d", c.CostMicros, wantMicros)
	}
	if c := computeCost(cand, openai, 0); c.CostMicros != wantMicros {
		t.Errorf("openai cost = %d micros, want %d", c.CostMicros, wantMicros)
	}
}
