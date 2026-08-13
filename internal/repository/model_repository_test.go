package repository

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

func TestCreateModelAndFindByID(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	m := &model.Model{Name: "smart", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}

	if err := CreateModel(db, m); err != nil {
		t.Fatalf("CreateModel failed: %v", err)
	}
	if m.ID == 0 {
		t.Fatalf("expected m.ID to be populated")
	}

	reloaded, err := FindModelByID(db, m.ID)
	if err != nil {
		t.Fatalf("FindModelByID failed: %v", err)
	}
	if reloaded.Name != "smart" {
		t.Fatalf("expected name 'smart', got %q", reloaded.Name)
	}
}

func TestCreateModelRejectsDuplicateName(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := CreateModel(db, &model.Model{Name: "smart", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("first CreateModel failed: %v", err)
	}
	err := CreateModel(db, &model.Model{Name: "smart", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now})
	if err == nil {
		t.Fatalf("expected a UNIQUE(name) violation on duplicate model name")
	}
}

func TestFindModelByNameReturnsNotFoundForUnknownName(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, err := FindModelByName(db, "does-not-exist")
	if err == nil {
		t.Fatalf("expected an error for an unknown model name")
	}
}

func TestListModelsOrdersByID(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := CreateModel(db, &model.Model{Name: "b-model", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateModel(b) failed: %v", err)
	}
	if err := CreateModel(db, &model.Model{Name: "a-model", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateModel(a) failed: %v", err)
	}
	list, err := ListModels(db)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(list) != 2 || list[0].Name != "b-model" || list[1].Name != "a-model" {
		t.Fatalf("expected id-ascending order [b-model, a-model], got %+v", list)
	}
}

func TestUpdateModelNameStatus(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	m := &model.Model{Name: "smart", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := CreateModel(db, m); err != nil {
		t.Fatalf("CreateModel failed: %v", err)
	}

	if err := UpdateModelNameStatus(db, m.ID, "smart-v2", model.ModelStatusDisabled, false, nil, now); err != nil {
		t.Fatalf("UpdateModelNameStatus failed: %v", err)
	}
	reloaded, err := FindModelByID(db, m.ID)
	if err != nil {
		t.Fatalf("FindModelByID failed: %v", err)
	}
	if reloaded.Name != "smart-v2" || reloaded.ManagementStatus != model.ModelStatusDisabled {
		t.Fatalf("expected name='smart-v2' status=disabled, got %+v", reloaded)
	}
}

// seedModelWithCandidate creates a Model and one ModelCandidate pointing at
// a freshly-seeded Provider+Key (via the existing seedProviderWithKey
// helper), for tests that need a realistic candidate row to act on.
func seedModelWithCandidate(t *testing.T, db *gorm.DB, modelName, providerName string) (*model.Model, *model.Provider, *model.ModelCandidate) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	m := &model.Model{Name: modelName, ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := CreateModel(db, m); err != nil {
		t.Fatalf("CreateModel failed: %v", err)
	}
	provider, _ := seedProviderWithKey(t, db, providerName)
	candidate := &model.ModelCandidate{
		ModelID: m.ID, ProviderID: provider.ID, ProviderModelName: "gpt-4o", SortOrder: 1,
		ManagementStatus: model.ModelCandidateStatusDisabled, VerificationStatus: model.ModelVerificationStatusUntested,
		CreatedAt: now, UpdatedAt: now, PriceUpdatedAt: now,
	}
	if err := CreateModelCandidate(db, candidate); err != nil {
		t.Fatalf("CreateModelCandidate failed: %v", err)
	}
	return m, provider, candidate
}

func TestCreateModelCandidateRejectsDuplicateProviderOnSameModel(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	dup := &model.ModelCandidate{
		ModelID: m.ID, ProviderID: provider.ID, ProviderModelName: "gpt-4o-mini", SortOrder: 2,
		ManagementStatus: model.ModelCandidateStatusDisabled, CreatedAt: now, UpdatedAt: now,
	}
	if err := CreateModelCandidate(db, dup); err == nil {
		t.Fatalf("expected a UNIQUE(model_id, provider_id) violation")
	}
}

func TestListModelCandidatesByModelIDPreloadsProvider(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")

	candidates, err := ListModelCandidatesByModelID(db, m.ID)
	if err != nil {
		t.Fatalf("ListModelCandidatesByModelID failed: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].Provider == nil || candidates[0].Provider.Name != provider.Name {
		t.Fatalf("expected Provider to be preloaded with name %q, got %+v", provider.Name, candidates[0].Provider)
	}
}

func TestListModelCandidatesByModelIDsReturnsNilForEmptyInput(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	candidates, err := ListModelCandidatesByModelIDs(db, nil)
	if err != nil {
		t.Fatalf("ListModelCandidatesByModelIDs failed: %v", err)
	}
	if candidates != nil {
		t.Fatalf("expected nil for empty input, got %+v", candidates)
	}
}

func TestListModelCandidatesByModelIDsGroupsAcrossMultipleModels(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m1, _, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	m2, _, _ := seedModelWithCandidate(t, db, "fast", "provider-b")

	candidates, err := ListModelCandidatesByModelIDs(db, []uint{m1.ID, m2.ID})
	if err != nil {
		t.Fatalf("ListModelCandidatesByModelIDs failed: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates across both models, got %d", len(candidates))
	}
}

func TestNextCandidateSortOrderStartsAtOne(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	m := &model.Model{Name: "smart", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := CreateModel(db, m); err != nil {
		t.Fatalf("CreateModel failed: %v", err)
	}
	next, err := NextCandidateSortOrder(db, m.ID)
	if err != nil {
		t.Fatalf("NextCandidateSortOrder failed: %v", err)
	}
	if next != 1 {
		t.Fatalf("expected 1 for a model with no candidates yet, got %d", next)
	}
}

func TestNextCandidateSortOrderIncrementsAfterExistingCandidate(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m, _, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	next, err := NextCandidateSortOrder(db, m.ID)
	if err != nil {
		t.Fatalf("NextCandidateSortOrder failed: %v", err)
	}
	if next != 2 {
		t.Fatalf("expected 2 after one existing candidate at sort_order=1, got %d", next)
	}
}

func TestUpdateModelCandidate(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, _, candidate := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)
	cacheWrite := 0.5
	cacheRead := 0.1

	if err := UpdateModelCandidate(db, candidate.ID, "gpt-4o-2024", 1.5, 3.0, &cacheWrite, &cacheRead, 4096, false, true, now); err != nil {
		t.Fatalf("UpdateModelCandidate failed: %v", err)
	}
	reloaded, err := FindModelCandidateByID(db, candidate.ID)
	if err != nil {
		t.Fatalf("FindModelCandidateByID failed: %v", err)
	}
	if reloaded.ProviderModelName != "gpt-4o-2024" || reloaded.InputPrice != 1.5 || reloaded.OutputPrice != 3.0 || reloaded.MaxOutput != 4096 {
		t.Fatalf("expected updated fields, got %+v", reloaded)
	}
	if reloaded.CacheWritePrice == nil || *reloaded.CacheWritePrice != 0.5 {
		t.Fatalf("expected cache_write_price=0.5, got %+v", reloaded.CacheWritePrice)
	}
}

func TestSetModelCandidateManagementStatus(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, _, candidate := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	if err := SetModelCandidateManagementStatus(db, candidate.ID, model.ModelCandidateStatusEnabled, now); err != nil {
		t.Fatalf("SetModelCandidateManagementStatus failed: %v", err)
	}
	reloaded, err := FindModelCandidateByID(db, candidate.ID)
	if err != nil {
		t.Fatalf("FindModelCandidateByID failed: %v", err)
	}
	if reloaded.ManagementStatus != model.ModelCandidateStatusEnabled {
		t.Fatalf("expected management_status=enabled, got %d", reloaded.ManagementStatus)
	}
}

// TestSetModelCandidateManagementStatusIfVerifiedAppliesWhenVerified and
// TestSetModelCandidateManagementStatusIfVerifiedSkipsWhenUntested are the
// direct regression tests for the CAS guard on enabling a candidate — the
// same check-then-act race class found and fixed for provider keys,
// applied here from the start.
func TestSetModelCandidateManagementStatusIfVerifiedAppliesWhenVerified(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, _, candidate := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Model(&model.ModelCandidate{}).Where("id = ?", candidate.ID).
		Update("verification_status", model.ModelVerificationStatusPassed).Error; err != nil {
		t.Fatalf("seed verification_status failed: %v", err)
	}

	applied, err := SetModelCandidateManagementStatusIfVerified(db, candidate.ID, model.ModelCandidateStatusEnabled, now)
	if err != nil {
		t.Fatalf("SetModelCandidateManagementStatusIfVerified failed: %v", err)
	}
	if !applied {
		t.Fatalf("expected applied=true when verification_status=Passed")
	}
}

func TestSetModelCandidateManagementStatusIfVerifiedSkipsWhenUntested(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, _, candidate := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	applied, err := SetModelCandidateManagementStatusIfVerified(db, candidate.ID, model.ModelCandidateStatusEnabled, now)
	if err != nil {
		t.Fatalf("SetModelCandidateManagementStatusIfVerified failed: %v", err)
	}
	if applied {
		t.Fatalf("expected applied=false when verification_status is still Untested")
	}
	reloaded, err := FindModelCandidateByID(db, candidate.ID)
	if err != nil {
		t.Fatalf("FindModelCandidateByID failed: %v", err)
	}
	if reloaded.ManagementStatus == model.ModelCandidateStatusEnabled {
		t.Fatalf("expected the write to be skipped, but management_status was changed to Enabled")
	}
}

// CommitModelCandidateProbeResults writes a whole probe run in one UPDATE, so
// last_test_result and last_tested_at describe the run as a whole rather than
// whichever probe happened to finish last.
func TestCommitModelCandidateProbeResultsWritesVerificationAndCapabilities(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, _, candidate := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)
	outcome := 0
	passed := model.ModelVerificationStatusPassed
	supported := true
	unsupported := false

	if _, err := CommitModelCandidateProbeResults(db, candidate.ID, "gpt-4o", CandidateProbeCommit{
		VerificationStatus:      &passed,
		LastTestResult:          &outcome,
		DurationMs:              42,
		SupportsStreaming:       &supported,
		SupportsFunctionCalling: &unsupported,
	}, now); err != nil {
		t.Fatalf("CommitModelCandidateProbeResults failed: %v", err)
	}
	reloaded, err := FindModelCandidateByID(db, candidate.ID)
	if err != nil {
		t.Fatalf("FindModelCandidateByID failed: %v", err)
	}
	if reloaded.VerificationStatus != model.ModelVerificationStatusPassed {
		t.Fatalf("expected verification_status=passed, got %d", reloaded.VerificationStatus)
	}
	if reloaded.LastTestResult == nil || *reloaded.LastTestResult != 0 {
		t.Fatalf("expected last_test_result=0, got %+v", reloaded.LastTestResult)
	}
	if reloaded.LastTestDurationMs == nil || *reloaded.LastTestDurationMs != 42 {
		t.Fatalf("expected last_test_duration_ms=42, got %+v", reloaded.LastTestDurationMs)
	}
	if reloaded.SupportsStreaming == nil || !*reloaded.SupportsStreaming {
		t.Fatalf("expected supports_streaming=true, got %v", reloaded.SupportsStreaming)
	}
	if reloaded.SupportsFunctionCalling == nil || *reloaded.SupportsFunctionCalling {
		t.Fatalf("expected supports_function_calling=false, got %v", reloaded.SupportsFunctionCalling)
	}
}

// A failing basic probe skips the capability probes, and that must leave
// previously earned verdicts alone rather than clearing them: a broken mapping
// is no evidence about what the mapping can do once it is fixed.
func TestCommitModelCandidateProbeResultsLeavesCapabilitiesAloneWhenNotProbed(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, _, candidate := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)
	outcome := 0
	passed := model.ModelVerificationStatusPassed
	supported := true

	if _, err := CommitModelCandidateProbeResults(db, candidate.ID, "gpt-4o", CandidateProbeCommit{
		VerificationStatus:      &passed,
		LastTestResult:          &outcome,
		SupportsStreaming:       &supported,
		SupportsFunctionCalling: &supported,
	}, now); err != nil {
		t.Fatalf("seed capability verdicts failed: %v", err)
	}
	authFailed := 1
	failed := model.ModelVerificationStatusFailed
	if _, err := CommitModelCandidateProbeResults(db, candidate.ID, "gpt-4o", CandidateProbeCommit{
		VerificationStatus: &failed,
		LastTestResult:     &authFailed,
		// Both capability verdicts nil: those probes were skipped, so the stored
		// values must survive.
	}, now); err != nil {
		t.Fatalf("CommitModelCandidateProbeResults(skipped capabilities) failed: %v", err)
	}

	reloaded, err := FindModelCandidateByID(db, candidate.ID)
	if err != nil {
		t.Fatalf("FindModelCandidateByID failed: %v", err)
	}
	if reloaded.VerificationStatus != model.ModelVerificationStatusFailed {
		t.Fatalf("expected verification_status=failed, got %d", reloaded.VerificationStatus)
	}
	if reloaded.SupportsStreaming == nil || !*reloaded.SupportsStreaming {
		t.Fatalf("expected supports_streaming to be preserved, got %v", reloaded.SupportsStreaming)
	}
	if reloaded.SupportsFunctionCalling == nil || !*reloaded.SupportsFunctionCalling {
		t.Fatalf("expected supports_function_calling to be preserved, got %v", reloaded.SupportsFunctionCalling)
	}
}

func TestSwapModelCandidateSortOrderSwapsNeighbors(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m, _, first := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	secondProvider, _ := seedProviderWithKey(t, db, "provider-b")
	second := &model.ModelCandidate{
		ModelID: m.ID, ProviderID: secondProvider.ID, ProviderModelName: "gpt-4o", SortOrder: 2,
		ManagementStatus: model.ModelCandidateStatusDisabled, CreatedAt: now, UpdatedAt: now,
	}
	if err := CreateModelCandidate(db, second); err != nil {
		t.Fatalf("CreateModelCandidate failed: %v", err)
	}

	applied, err := SwapModelCandidateSortOrder(db, m.ID, second.ID, "up")
	if err != nil {
		t.Fatalf("SwapModelCandidateSortOrder failed: %v", err)
	}
	if !applied {
		t.Fatalf("expected applied=true")
	}

	reloadedFirst, err := FindModelCandidateByID(db, first.ID)
	if err != nil {
		t.Fatalf("FindModelCandidateByID failed: %v", err)
	}
	if reloadedFirst.SortOrder != 2 {
		t.Fatalf("expected first candidate to now have sort_order=2, got %d", reloadedFirst.SortOrder)
	}
	reloadedSecond, err := FindModelCandidateByID(db, second.ID)
	if err != nil {
		t.Fatalf("FindModelCandidateByID failed: %v", err)
	}
	if reloadedSecond.SortOrder != 1 {
		t.Fatalf("expected second candidate to now have sort_order=1, got %d", reloadedSecond.SortOrder)
	}
}

func TestSwapModelCandidateSortOrderNoopAtBoundary(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m, _, first := seedModelWithCandidate(t, db, "smart", "provider-a")

	applied, err := SwapModelCandidateSortOrder(db, m.ID, first.ID, "up")
	if err != nil {
		t.Fatalf("SwapModelCandidateSortOrder failed: %v", err)
	}
	if applied {
		t.Fatalf("expected applied=false at the top boundary")
	}
}

func TestSwapModelCandidateSortOrderReturnsErrorForUnknownCandidate(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m, _, _ := seedModelWithCandidate(t, db, "smart", "provider-a")

	_, err := SwapModelCandidateSortOrder(db, m.ID, 999999, "up")
	if err == nil {
		t.Fatalf("expected an error for an unknown candidate ID")
	}
}

func TestFindModelByIDReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	testutil.CloseDB(t, db)
	if _, err := FindModelByID(db, 1); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

func TestFindModelByNameReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	testutil.CloseDB(t, db)
	if _, err := FindModelByName(db, "smart"); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

func TestListModelsReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	testutil.CloseDB(t, db)
	if _, err := ListModels(db); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

func TestListModelCandidatesByModelIDReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	testutil.CloseDB(t, db)
	if _, err := ListModelCandidatesByModelID(db, 1); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

func TestListModelCandidatesByModelIDsReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	testutil.CloseDB(t, db)
	if _, err := ListModelCandidatesByModelIDs(db, []uint{1}); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

func TestNextCandidateSortOrderReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	testutil.CloseDB(t, db)
	if _, err := NextCandidateSortOrder(db, 1); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

func TestSetModelCandidateManagementStatusIfVerifiedReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	testutil.CloseDB(t, db)
	if _, err := SetModelCandidateManagementStatusIfVerified(db, 1, model.ModelCandidateStatusEnabled, time.Now().UTC()); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

func TestSwapModelCandidateSortOrderReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m, _, first := seedModelWithCandidate(t, db, "smart", "provider-a")
	testutil.CloseDB(t, db)

	if _, err := SwapModelCandidateSortOrder(db, m.ID, first.ID, "up"); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

func TestDeleteModelCandidate(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, _, candidate := seedModelWithCandidate(t, db, "smart", "provider-a")

	if err := DeleteModelCandidate(db, candidate.ID); err != nil {
		t.Fatalf("DeleteModelCandidate failed: %v", err)
	}
	if _, err := FindModelCandidateByID(db, candidate.ID); err == nil {
		t.Fatalf("expected the candidate to be gone after deletion")
	}
}

// seedCandidatePrice attaches one more model to an existing provider, carrying
// the given upstream name and prices. A second candidate on the same provider
// needs its own model row because of UNIQUE(model_id, provider_id).
func seedCandidatePrice(
	t *testing.T, db *gorm.DB, providerID uint, modelName, providerModelName string,
	inputPrice, outputPrice float64, updatedAt time.Time,
) *model.ModelCandidate {
	t.Helper()
	m := &model.Model{Name: modelName, ManagementStatus: model.ModelStatusEnabled, CreatedAt: updatedAt, UpdatedAt: updatedAt}
	if err := CreateModel(db, m); err != nil {
		t.Fatalf("CreateModel failed: %v", err)
	}
	c := &model.ModelCandidate{
		ModelID: m.ID, ProviderID: providerID, ProviderModelName: providerModelName,
		InputPrice: inputPrice, OutputPrice: outputPrice, SortOrder: 1,
		ManagementStatus: model.ModelCandidateStatusDisabled, VerificationStatus: model.ModelVerificationStatusUntested,
		CreatedAt: updatedAt, UpdatedAt: updatedAt, PriceUpdatedAt: updatedAt,
	}
	if err := CreateModelCandidate(db, c); err != nil {
		t.Fatalf("CreateModelCandidate failed: %v", err)
	}
	return c
}

func TestFindLatestCandidatePriceReturnsMostRecentlyUpdated(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	seedCandidatePrice(t, db, provider.ID, "older-alias", "deepseek-v4-pro", 1, 2, now.Add(-time.Hour))
	seedCandidatePrice(t, db, provider.ID, "newer-alias", "deepseek-v4-pro", 3, 6, now)

	got, err := FindLatestCandidatePrice(db, provider.ID, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("FindLatestCandidatePrice failed: %v", err)
	}
	if got.InputPrice != 3 || got.OutputPrice != 6 {
		t.Fatalf("want the newest row's prices 3/6, got %v/%v", got.InputPrice, got.OutputPrice)
	}
	// Documented contract: the row is a price carrier, so anything outside the
	// selected columns stays zero and must not be read. provider_model_name IS
	// selected — it is what the case-insensitive match is done against.
	if got.ModelID != 0 || got.SortOrder != 0 || !got.UpdatedAt.IsZero() {
		t.Fatalf("expected only the look-up columns to be populated, got %+v", got)
	}
}

// Upstream model names are quoted inconsistently. A byte-exact match would miss
// the provider's own negotiated price and silently fall through to the seed
// catalog's generic figure, which is exactly the wrong way round.
func TestFindLatestCandidatePriceMatchesNameCaseInsensitively(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)
	seedCandidatePrice(t, db, provider.ID, "alias", "DeepSeek-V4-Pro", 2.4, 4.8, now)

	for _, name := range []string{"deepseek-v4-pro", "DEEPSEEK-V4-PRO", "DeepSeek-V4-Pro"} {
		got, err := FindLatestCandidatePrice(db, provider.ID, name)
		if err != nil {
			t.Fatalf("FindLatestCandidatePrice(%q) failed: %v", name, err)
		}
		if got.InputPrice != 2.4 || got.OutputPrice != 4.8 {
			t.Errorf("%q: want 2.4/4.8, got %v/%v", name, got.InputPrice, got.OutputPrice)
		}
	}
}

func TestFindLatestCandidatePriceIsScopedToOneProvider(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, providerA, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	providerB, _ := seedProviderWithKey(t, db, "provider-b")
	now := time.Now().UTC().Truncate(time.Second)
	seedCandidatePrice(t, db, providerA.ID, "alias-a", "deepseek-v4-pro", 1, 2, now)

	// Prices follow the provider: B's own resale price is not A's.
	if _, err := FindLatestCandidatePrice(db, providerB.ID, "deepseek-v4-pro"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected ErrRecordNotFound for a different provider, got %v", err)
	}
}

func TestFindLatestCandidatePriceReturnsNotFoundForUnknownModel(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")

	if _, err := FindLatestCandidatePrice(db, provider.ID, "never-configured"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected ErrRecordNotFound, got %v", err)
	}
}

func TestFindLatestCandidatePriceReturnsErrorWhenDBUnavailable(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	testutil.CloseDB(t, db)

	if _, err := FindLatestCandidatePrice(db, provider.ID, "gpt-4o"); err == nil {
		t.Fatalf("expected an error once the underlying connection is closed")
	}
}

// updated_at moves for reasons that have nothing to do with money — enabling a
// candidate, retesting it, committing a probe result. If price recency were read
// from that column, any of those on an old candidate would resurrect its stale
// rate and auto-fill it into the next candidate for the same upstream model.
func TestFindLatestCandidatePriceIgnoresNonPriceUpdates(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	stale := seedCandidatePrice(t, db, provider.ID, "older-alias", "deepseek-v4-pro", 1, 2, now.Add(-time.Hour))
	seedCandidatePrice(t, db, provider.ID, "newer-alias", "deepseek-v4-pro", 3, 6, now)

	// Toggling the older candidate's status bumps its updated_at past the newer
	// one's without restating its price.
	if err := SetModelCandidateManagementStatus(db, stale.ID, model.ModelCandidateStatusEnabled, now.Add(time.Hour)); err != nil {
		t.Fatalf("SetModelCandidateManagementStatus failed: %v", err)
	}

	got, err := FindLatestCandidatePrice(db, provider.ID, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("FindLatestCandidatePrice failed: %v", err)
	}
	if got.InputPrice != 3 || got.OutputPrice != 6 {
		t.Fatalf("want the most recently priced 3/6, got %v/%v", got.InputPrice, got.OutputPrice)
	}
}

// The other half of the same rule: an edit that DOES restate a price makes that
// candidate the newest, even though it was created first.
func TestFindLatestCandidatePriceFollowsPriceEdits(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	older := seedCandidatePrice(t, db, provider.ID, "older-alias", "deepseek-v4-pro", 1, 2, now.Add(-time.Hour))
	seedCandidatePrice(t, db, provider.ID, "newer-alias", "deepseek-v4-pro", 3, 6, now)

	if err := UpdateModelCandidate(db, older.ID, "deepseek-v4-pro", 2.4, 4.8, nil, nil, 0, false, true, now.Add(time.Hour)); err != nil {
		t.Fatalf("UpdateModelCandidate failed: %v", err)
	}

	got, err := FindLatestCandidatePrice(db, provider.ID, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("FindLatestCandidatePrice failed: %v", err)
	}
	if got.InputPrice != 2.4 || got.OutputPrice != 4.8 {
		t.Fatalf("want the freshly restated 2.4/4.8, got %v/%v", got.InputPrice, got.OutputPrice)
	}
}

// GORM writes every mapped column on insert rather than omitting an unset one,
// so a candidate built without a price clock would land on year 0001 and lose
// every recency comparison forever. The model's BeforeCreate hook is what stops
// that; this pins it, because the failure is silent.
func TestCreateModelCandidateNeverStoresAZeroPriceClock(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	m, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	other := &model.Model{Name: "other", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := CreateModel(db, other); err != nil {
		t.Fatalf("CreateModel failed: %v", err)
	}
	_ = m
	c := &model.ModelCandidate{
		ModelID: other.ID, ProviderID: provider.ID, ProviderModelName: "gpt-4o", SortOrder: 1,
		ManagementStatus: model.ModelCandidateStatusDisabled, CreatedAt: now, UpdatedAt: now,
	}
	if err := CreateModelCandidate(db, c); err != nil {
		t.Fatalf("CreateModelCandidate failed: %v", err)
	}

	stored, err := FindModelCandidateByID(db, c.ID)
	if err != nil {
		t.Fatalf("FindModelCandidateByID failed: %v", err)
	}
	if stored.PriceUpdatedAt.IsZero() || stored.PriceUpdatedAt.Year() < 2000 {
		t.Fatalf("expected the price clock to be stamped, got %v", stored.PriceUpdatedAt)
	}
}

// The candidate form always posts the whole record, so an edit that touches only
// max_output or the enabled switch re-sends the same prices. Advancing the price
// clock there would let a candidate nobody repriced overtake one that was.
func TestUpdateModelCandidateLeavesPriceClockAloneWhenPricesAreUnchanged(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	stale := seedCandidatePrice(t, db, provider.ID, "older-alias", "deepseek-v4-pro", 1, 2, now.Add(-time.Hour))
	seedCandidatePrice(t, db, provider.ID, "newer-alias", "deepseek-v4-pro", 3, 6, now)

	// Same prices, different max_output: priceChanged is false.
	if err := UpdateModelCandidate(db, stale.ID, "deepseek-v4-pro", 1, 2, nil, nil, 4096, false, false, now.Add(time.Hour)); err != nil {
		t.Fatalf("UpdateModelCandidate failed: %v", err)
	}

	got, err := FindLatestCandidatePrice(db, provider.ID, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("FindLatestCandidatePrice failed: %v", err)
	}
	if got.InputPrice != 3 || got.OutputPrice != 6 {
		t.Fatalf("a non-price edit promoted the stale rate: got %v/%v, want 3/6", got.InputPrice, got.OutputPrice)
	}
}

// The stored fold has to behave the same for names SQL LOWER() would disagree
// on across backends — that divergence is why the match is against a folded
// column written by Go rather than a LOWER() in the predicate.
func TestFindLatestCandidatePriceFoldsNonASCIINames(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)
	seedCandidatePrice(t, db, provider.ID, "alias", "Модель-Про", 2.4, 4.8, now)

	got, err := FindLatestCandidatePrice(db, provider.ID, "модель-про")
	if err != nil {
		t.Fatalf("FindLatestCandidatePrice failed: %v", err)
	}
	if got.InputPrice != 2.4 {
		t.Fatalf("want 2.4 from the folded match, got %v", got.InputPrice)
	}
}

// The folded name is derived, so nothing may write one column without the other:
// a stale copy makes the row silently invisible to price suggestions.
func TestUpdateModelCandidateKeepsTheFoldedNameInStep(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)
	c := seedCandidatePrice(t, db, provider.ID, "alias", "deepseek-v4-flash", 1, 2, now)

	if err := UpdateModelCandidate(db, c.ID, "DeepSeek-V4-Pro", 3, 6, nil, nil, 0, true, true, now.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateModelCandidate failed: %v", err)
	}

	// Findable under the new name...
	got, err := FindLatestCandidatePrice(db, provider.ID, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("FindLatestCandidatePrice after retarget failed: %v", err)
	}
	if got.InputPrice != 3 || got.OutputPrice != 6 {
		t.Fatalf("want 3/6 under the new name, got %v/%v", got.InputPrice, got.OutputPrice)
	}
	// ...and no longer under the old one.
	if _, err := FindLatestCandidatePrice(db, provider.ID, "deepseek-v4-flash"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected the old name to stop matching, got %v", err)
	}
}

// Retargeting restates the price for a model this row never priced before, so it
// has to advance the clock even when every number stayed the same.
func TestUpdateModelCandidateStampsPriceClockOnRetarget(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, provider, _ := seedModelWithCandidate(t, db, "smart", "provider-a")
	now := time.Now().UTC().Truncate(time.Second)

	// An existing, more recently priced candidate for the name we retarget onto.
	seedCandidatePrice(t, db, provider.ID, "incumbent", "deepseek-v4-pro", 3, 6, now)
	// An older candidate on a different name, carrying the rate we keep.
	moved := seedCandidatePrice(t, db, provider.ID, "moved", "deepseek-v4-flash", 1, 2, now.Add(-time.Hour))

	// Same numbers, new target: priceChanged is driven by targetChanged.
	if err := UpdateModelCandidate(db, moved.ID, "deepseek-v4-pro", 1, 2, nil, nil, 0, true, true, now.Add(time.Hour)); err != nil {
		t.Fatalf("UpdateModelCandidate failed: %v", err)
	}

	got, err := FindLatestCandidatePrice(db, provider.ID, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("FindLatestCandidatePrice failed: %v", err)
	}
	if got.InputPrice != 1 || got.OutputPrice != 2 {
		t.Fatalf("the retargeted row should be newest for this name, got %v/%v", got.InputPrice, got.OutputPrice)
	}
}
