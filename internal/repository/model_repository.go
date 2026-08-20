// Package repository provides Model / ModelCandidate pure data access.
package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
)

func FindModelByID(db *gorm.DB, id uint) (*model.Model, error) {
	var m model.Model
	if err := db.Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func FindModelByName(db *gorm.DB, name string) (*model.Model, error) {
	var m model.Model
	if err := db.Where("name = ?", name).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func ListModels(db *gorm.DB) ([]model.Model, error) {
	var models []model.Model
	if err := db.Order("id ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

func CreateModel(db *gorm.DB, m *model.Model) error {
	return db.Create(m).Error
}

// UpdateModelNameStatus writes name/status — and, when imageInputSet is
// true, the tri-state image-input declaration — in ONE statement, so a
// concurrent PATCH can never interleave the fields into a row neither
// caller submitted. imageInput nil with imageInputSet=true stores NULL
// (clearing a previous declaration); imageInputSet=false leaves the column
// untouched.
func UpdateModelNameStatus(db *gorm.DB, id uint, name string, status int, imageInputSet bool, imageInput *bool, now time.Time) error {
	updates := map[string]interface{}{"name": name, "management_status": status, "updated_at": now}
	if imageInputSet {
		updates["supports_image_input"] = imageInput
	}
	return db.Model(&model.Model{}).Where("id = ?", id).Updates(updates).Error
}

func ListModelCandidatesByModelID(db *gorm.DB, modelID uint) ([]model.ModelCandidate, error) {
	var candidates []model.ModelCandidate
	if err := db.Preload("Provider").Where("model_id = ?", modelID).Order("sort_order ASC").Find(&candidates).Error; err != nil {
		return nil, err
	}
	return candidates, nil
}

// ListModelCandidatesByModelIDs batches the N+1 that a naive per-model
// candidate lookup would cause when listing models (the same fix used for
// ListProviderKeysByProviderIDs).
func ListModelCandidatesByModelIDs(db *gorm.DB, modelIDs []uint) ([]model.ModelCandidate, error) {
	if len(modelIDs) == 0 {
		return nil, nil
	}
	var candidates []model.ModelCandidate
	if err := db.Preload("Provider").Where("model_id IN ?", modelIDs).Order("model_id ASC, sort_order ASC").Find(&candidates).Error; err != nil {
		return nil, err
	}
	return candidates, nil
}

// ListModelsByNames resolves a set of names in one query — the bulk-import
// preload that replaces a per-name FindModelByName loop.
func ListModelsByNames(db *gorm.DB, names []string) ([]model.Model, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var models []model.Model
	if err := db.Where("name IN ?", names).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// bulkInsertBatchSize caps rows per INSERT so the widest table stays far below
// SQLite's 32,766 bind-variable limit (ModelCandidate maps ~20 columns; one
// unchunked 2,000-row insert would need ~40,000 variables and fail). The
// batches still run inside whatever transaction the caller opened, so
// atomicity is unchanged.
const bulkInsertBatchSize = 500

// CreateModelsBulk inserts the rows in bounded batches. GORM fills the primary
// keys back on both supported backends; per-row hooks still run.
func CreateModelsBulk(db *gorm.DB, models []*model.Model) error {
	if len(models) == 0 {
		return nil
	}
	return db.CreateInBatches(models, bulkInsertBatchSize).Error
}

// CreateModelCandidatesBulk is CreateModelsBulk for candidates; BeforeCreate
// runs per row, so the folded-name and price-clock guarantees hold for bulk
// inserts too.
func CreateModelCandidatesBulk(db *gorm.DB, candidates []*model.ModelCandidate) error {
	if len(candidates) == 0 {
		return nil
	}
	return db.CreateInBatches(candidates, bulkInsertBatchSize).Error
}

// ListMappedModelIDs returns which of the given models already have a mapping
// for this provider — one membership query instead of a per-model existence
// check.
func ListMappedModelIDs(db *gorm.DB, providerID uint, modelIDs []uint) (map[uint]bool, error) {
	if len(modelIDs) == 0 {
		return map[uint]bool{}, nil
	}
	var ids []uint
	if err := db.Model(&model.ModelCandidate{}).Where("provider_id = ? AND model_id IN ?", providerID, modelIDs).
		Pluck("model_id", &ids).Error; err != nil {
		return nil, err
	}
	mapped := make(map[uint]bool, len(ids))
	for _, id := range ids {
		mapped[id] = true
	}
	return mapped, nil
}

// MaxCandidateSortOrders returns each model's current highest sort_order in
// one grouped query; models with no candidates are simply absent (treat as 0).
func MaxCandidateSortOrders(db *gorm.DB, modelIDs []uint) (map[uint]int, error) {
	if len(modelIDs) == 0 {
		return map[uint]int{}, nil
	}
	var rows []struct {
		ModelID  uint
		MaxOrder int
	}
	if err := db.Model(&model.ModelCandidate{}).
		Select("model_id, COALESCE(MAX(sort_order), 0) AS max_order").
		Where("model_id IN ?", modelIDs).Group("model_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	orders := make(map[uint]int, len(rows))
	for _, r := range rows {
		orders[r.ModelID] = r.MaxOrder
	}
	return orders, nil
}

func FindModelCandidateByID(db *gorm.DB, id uint) (*model.ModelCandidate, error) {
	var c model.ModelCandidate
	if err := db.Where("id = ?", id).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func NextCandidateSortOrder(db *gorm.DB, modelID uint) (int, error) {
	var maxOrder int
	if err := db.Model(&model.ModelCandidate{}).Where("model_id = ?", modelID).
		Select("COALESCE(MAX(sort_order), 0)").Scan(&maxOrder).Error; err != nil {
		return 0, err
	}
	return maxOrder + 1, nil
}

func CreateModelCandidate(db *gorm.DB, c *model.ModelCandidate) error {
	return db.Create(c).Error
}

// UpdateModelCandidate writes the edited fields. priceChanged must be true only
// when one of the four price values actually differs from what is stored: the
// price clock is what the auto-suggest look-up orders by, so stamping it on an
// edit that merely re-sent the same numbers (which every save from the candidate
// form does — it always posts the full record) would let a candidate nobody
// repriced overtake one that was, and resurrect its stale rate.
func UpdateModelCandidate(db *gorm.DB, id uint, providerModelName string, inputPrice, outputPrice float64,
	cacheWritePrice, cacheReadPrice *float64, maxOutput int, resetVerification, priceChanged bool, now time.Time) error {
	updates := map[string]interface{}{
		"provider_model_name": providerModelName,
		// Never written without its source column: a row whose folded copy is
		// stale is silently invisible to the price look-up.
		"provider_model_name_folded": model.FoldModelName(providerModelName),
		"input_price":                inputPrice,
		"output_price":               outputPrice,
		"cache_write_price":          cacheWritePrice,
		"cache_read_price":           cacheReadPrice,
		"max_output":                 maxOutput,
		"updated_at":                 now,
	}
	if priceChanged {
		updates["price_updated_at"] = now
	}
	if resetVerification {
		// The mapping test and capability probes validated the OLD
		// provider_model_name; a new name makes them stale, so clear them —
		// the candidate must be re-tested before it can route or be enabled
		// again. A map-based Updates writes these values (a struct-based one
		// would skip them).
		//
		// Capabilities clear to NULL ("not confirmed"), not false: nothing about
		// the old name's confirmation carries over to a new one, and false would
		// assert a decisive "not supported" that no probe established.
		updates["verification_status"] = model.ModelVerificationStatusUntested
		updates["supports_streaming"] = nil
		updates["supports_function_calling"] = nil
	}
	return db.Model(&model.ModelCandidate{}).Where("id = ?", id).Updates(updates).Error
}

func SetModelCandidateManagementStatus(db *gorm.DB, id uint, status int, now time.Time) error {
	return db.Model(&model.ModelCandidate{}).Where("id = ?", id).
		Updates(map[string]interface{}{"management_status": status, "updated_at": now}).Error
}

// FindLatestCandidatePrice returns the most recently PRICED candidate row for
// the given provider + provider_model_name, used to auto-suggest prices when
// adding a new candidate for the same model (prices follow the provider).
// Returns gorm.ErrRecordNotFound when no such candidate exists — the caller
// then falls back to the built-in seed catalog.
//
// Recency is price_updated_at, not updated_at. Enabling, disabling, retesting
// and probing all bump updated_at without touching a price, so ordering by it
// would let a retest on an old candidate promote its stale rate over a newer
// one. Ties break on id so the answer is deterministic rather than
// storage-order dependent.
//
// The name is matched case-insensitively, matching pricecatalog.Lookup: upstream
// model names are quoted inconsistently ("DeepSeek-V4-Pro" vs "deepseek-v4-pro")
// and a byte-exact match would miss the provider's own negotiated price and fall
// through to the catalog's generic figure — inverting the intended precedence.
// The comparison runs against the stored folded copy rather than a SQL LOWER()
// of the name, because LOWER() is not the same function on both supported
// backends and the same data would otherwise match on one and miss on the other.
// Both sides go through model.FoldModelName, and the predicate stays a plain
// equality the index can seek on — one row read, not a scan of the provider's
// whole catalogue on every keystroke in the candidate form.
//
// Only the columns needed to answer the question are selected; the row is a
// price carrier, not a full candidate, so everything else stays zero-valued and
// must not be relied on.
// FindLatestCandidatePricesByFoldedNames is the batch form of
// FindLatestCandidatePrice: one query for a whole import dialog instead of one
// per row. Keys of the returned map are folded names; a name with no history
// is simply absent. The recency rule is identical to the single look-up
// (price_updated_at DESC, id DESC): rows arrive in that order and the first
// row seen per folded name wins.
func FindLatestCandidatePricesByFoldedNames(db *gorm.DB, providerID uint, foldedNames []string) (map[string]model.ModelCandidate, error) {
	if len(foldedNames) == 0 {
		return map[string]model.ModelCandidate{}, nil
	}
	var rows []model.ModelCandidate
	err := db.
		Select("input_price", "output_price", "cache_write_price", "cache_read_price", "price_updated_at", "provider_model_name_folded").
		Where("provider_id = ? AND provider_model_name_folded IN ?", providerID, foldedNames).
		Order("price_updated_at DESC, id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	latest := make(map[string]model.ModelCandidate, len(rows))
	for _, r := range rows {
		if _, seen := latest[r.ProviderModelNameFolded]; !seen {
			latest[r.ProviderModelNameFolded] = r
		}
	}
	return latest, nil
}

func FindLatestCandidatePrice(db *gorm.DB, providerID uint, providerModelName string) (*model.ModelCandidate, error) {
	folded := model.FoldModelName(providerModelName)
	if folded == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var c model.ModelCandidate
	err := db.
		Select("input_price", "output_price", "cache_write_price", "cache_read_price", "price_updated_at").
		Where("provider_id = ? AND provider_model_name_folded = ?", providerID, folded).
		// Take rather than First: First appends its own ascending primary-key
		// ordering, which would fight the descending tie-breaker here.
		Order("price_updated_at DESC, id DESC").
		Take(&c).Error
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SetModelCandidateManagementStatusIfVerified CAS-guards enabling a candidate on
// verification_status still reading Passed — closing the check-then-act window
// between the service layer's gate check and the write.
func SetModelCandidateManagementStatusIfVerified(db *gorm.DB, id uint, status int, now time.Time) (bool, error) {
	result := db.Model(&model.ModelCandidate{}).
		Where("id = ? AND verification_status = ?", id, model.ModelVerificationStatusPassed).
		Updates(map[string]interface{}{"management_status": status, "updated_at": now})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// EnableModelCandidateAfterProbe enables a candidate only if it is still the row
// that was probed. Verification must read Passed, provider_model_name must still
// be the name the probe actually tested, and management_status must still hold the
// value observed before the probe started.
//
// All three matter because a probe takes seconds of upstream round trips, during
// which the row can move underneath it. Without the name guard an earlier probe
// could mark a later, different mapping as verified and enable it — routing a
// target nobody tested. Without the management-status guard an operator who
// disabled the candidate mid-probe would find it silently re-enabled, since
// verification_status alone says nothing about their intent.
//
// applied is false when any guard misses, which callers must treat as "the row
// moved on, discard this result" rather than as an error.
func EnableModelCandidateAfterProbe(db *gorm.DB, id uint, probedModelName string, expectedManagementStatus, status int, now time.Time) (bool, error) {
	result := db.Model(&model.ModelCandidate{}).
		Where("id = ? AND verification_status = ? AND provider_model_name = ? AND management_status = ?",
			id, model.ModelVerificationStatusPassed, probedModelName, expectedManagementStatus).
		Updates(map[string]interface{}{"management_status": status, "updated_at": now})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// CandidateProbeCommit is what to persist after a full probe run (basic plus
// the two capability probes), written in one UPDATE so last_test_result and
// last_tested_at describe the run as a whole. Committing the three probes
// through separate calls would leave those shared columns holding whichever
// probe happened to finish last.
// Every verdict field is nil-means-leave-alone. A probe that did not run, or ran
// and came back inconclusive, is no evidence at all, and writing it would throw
// away a verdict that was previously earned. For verification_status that is not
// cosmetic — the gateway drops a candidate whose status is not Passed from ALL
// routing. Callers classify; this struct only carries what they decided is worth
// persisting.
type CandidateProbeCommit struct {
	VerificationStatus      *int
	SupportsStreaming       *bool
	SupportsFunctionCalling *bool
	LastTestResult          *int
	DurationMs              int64
}

// CommitModelCandidateProbeResults persists a whole probe run, but only if the
// candidate still points at probedModelName — the mapping the probe actually
// tested.
//
// The guard is what keeps a slow probe from landing on a configuration it never
// examined: probing takes seconds of upstream round trips, so a concurrent edit
// can retarget the row in the meantime, and an id-only write would then stamp the
// new target with the old target's verdict. applied is false in that case, which
// callers must treat as "discard this result" rather than as an error — the edit
// that moved the row runs its own probe.
func CommitModelCandidateProbeResults(db *gorm.DB, id uint, probedModelName string, c CandidateProbeCommit, now time.Time) (bool, error) {
	updates := map[string]interface{}{
		"last_test_result":      c.LastTestResult,
		"last_test_duration_ms": c.DurationMs,
		"last_tested_at":        now,
		"updated_at":            now,
	}
	if c.VerificationStatus != nil {
		updates["verification_status"] = *c.VerificationStatus
	}
	if c.SupportsStreaming != nil {
		updates["supports_streaming"] = *c.SupportsStreaming
	}
	if c.SupportsFunctionCalling != nil {
		updates["supports_function_calling"] = *c.SupportsFunctionCalling
	}
	result := db.Model(&model.ModelCandidate{}).
		Where("id = ? AND provider_model_name = ?", id, probedModelName).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// SwapModelCandidateSortOrder atomically swaps sort_order between
// candidateID and its immediate neighbor in the given direction, scoped to
// modelID (a candidate's route-chain position is only meaningful within
// its own model). Same intermediate-negative-value pattern
// as SwapProviderKeySortOrder to avoid momentarily violating
// UNIQUE(model_id, sort_order) mid-swap.
func SwapModelCandidateSortOrder(db *gorm.DB, modelID, candidateID uint, direction string) (bool, error) {
	var applied bool
	err := db.Transaction(func(tx *gorm.DB) error {
		var current model.ModelCandidate
		if err := tx.Select("id, sort_order").Where("id = ? AND model_id = ?", candidateID, modelID).First(&current).Error; err != nil {
			return err
		}

		var neighbor model.ModelCandidate
		var neighborErr error
		if direction == "up" {
			neighborErr = tx.Select("id, sort_order").Where("model_id = ? AND sort_order < ?", modelID, current.SortOrder).
				Order("sort_order DESC").Limit(1).First(&neighbor).Error
		} else {
			neighborErr = tx.Select("id, sort_order").Where("model_id = ? AND sort_order > ?", modelID, current.SortOrder).
				Order("sort_order ASC").Limit(1).First(&neighbor).Error
		}
		if errors.Is(neighborErr, gorm.ErrRecordNotFound) {
			applied = false
			return nil
		}
		if neighborErr != nil {
			return neighborErr
		}

		const tempOrder = -1
		if err := tx.Model(&model.ModelCandidate{}).Where("id = ?", current.ID).Update("sort_order", tempOrder).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ModelCandidate{}).Where("id = ?", neighbor.ID).Update("sort_order", current.SortOrder).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ModelCandidate{}).Where("id = ?", current.ID).Update("sort_order", neighbor.SortOrder).Error; err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

func DeleteModelCandidate(db *gorm.DB, id uint) error {
	return db.Where("id = ?", id).Delete(&model.ModelCandidate{}).Error
}

// ListModelCandidatesByProviderID returns every candidate on one provider,
// across all models and management states — the reference list an impact
// preview starts from when the provider is about to be disabled.
func ListModelCandidatesByProviderID(db *gorm.DB, providerID uint) ([]model.ModelCandidate, error) {
	var candidates []model.ModelCandidate
	if err := db.Where("provider_id = ?", providerID).Order("model_id ASC, sort_order ASC").Find(&candidates).Error; err != nil {
		return nil, err
	}
	return candidates, nil
}

// ListModelsByIDs batch-fetches models by id (the same N+1 fix as
// ListModelCandidatesByModelIDs). Empty input returns nil without querying.
func ListModelsByIDs(db *gorm.DB, ids []uint) ([]model.Model, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var models []model.Model
	if err := db.Where("id IN ?", ids).Order("id ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}
