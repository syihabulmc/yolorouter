package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
)

func seedProviderRow(t *testing.T, db *gorm.DB, name string) *model.Provider {
	t.Helper()
	now := time.Now().UTC()
	p := &model.Provider{
		Name: name, ProviderType: "openai", BaseURL: "https://example.invalid",
		ManagementStatus: model.ProviderStatusEnabled, DestinationVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed provider failed: %v", err)
	}
	return p
}

func TestPostProviderModelsImportCreatesAndReports(t *testing.T) {
	r, db := newModelTestRouter(t)
	p := seedProviderRow(t, db, "prov-import")

	w, env := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/admin/providers/%d/models/import", p.ID), map[string]interface{}{
		"items": []map[string]interface{}{
			{"provider_model_name": "deepseek-ai/DeepSeek-V4", "input_price": 2, "output_price": 8},
		},
	}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var result struct {
		Items []struct {
			Name        string `json:"name"`
			Status      string `json:"status"`
			ModelID     uint   `json:"model_id"`
			CandidateID uint   `json:"candidate_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(env.Data, &result); err != nil {
		t.Fatalf("unmarshal import response: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Status != "created" || result.Items[0].CandidateID == 0 {
		t.Fatalf("expected one created item with a candidate id, got %+v", result)
	}
}

func TestPostProviderModelsImportRejectsEmptyItems(t *testing.T) {
	r, db := newModelTestRouter(t)
	p := seedProviderRow(t, db, "prov-import")
	w, _ := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/admin/providers/%d/models/import", p.ID),
		map[string]interface{}{"items": []map[string]interface{}{}}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty items list, got %d", w.Code)
	}
}

func TestPostProviderSuggestPricesRejectsEmptyName(t *testing.T) {
	r, db := newModelTestRouter(t)
	p := seedProviderRow(t, db, "prov-prices")
	w, _ := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/admin/providers/%d/models/suggest-prices", p.ID),
		map[string]interface{}{"names": []string{""}}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty name entry, got %d", w.Code)
	}
}

func TestPostProviderSuggestPricesReturnsEntryPerName(t *testing.T) {
	r, db := newModelTestRouter(t)
	p := seedProviderRow(t, db, "prov-prices")

	w, env := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/admin/providers/%d/models/suggest-prices", p.ID),
		map[string]interface{}{"names": []string{"some-model"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var result struct {
		Prices map[string]struct {
			Source string `json:"source"`
		} `json:"prices"`
	}
	if err := json.Unmarshal(env.Data, &result); err != nil {
		t.Fatalf("unmarshal suggest-prices response: %v", err)
	}
	entry, ok := result.Prices["some-model"]
	if !ok || entry.Source != "" {
		t.Fatalf("expected an empty-source entry for the unmatched name, got %+v", result)
	}
}

// Upstream catalogs really do exceed 500 ids (the provider client follows up
// to 20 pages with no per-page cap), so a full-catalog batch must not be
// rejected at the binding layer.
func TestPostProviderModelsImportAcceptsFullCatalogSizedBatch(t *testing.T) {
	r, db := newModelTestRouter(t)
	p := seedProviderRow(t, db, "prov-import")
	items := make([]map[string]interface{}, 501)
	for i := range items {
		items[i] = map[string]interface{}{"provider_model_name": fmt.Sprintf("vendor/model-%d", i), "input_price": 1, "output_price": 2}
	}
	w, _ := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/admin/providers/%d/models/import", p.ID),
		map[string]interface{}{"items": items}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for a 501-item catalog import, got %d, body: %s", w.Code, w.Body.String()[:200])
	}
}

func TestPostProviderSuggestPricesAcceptsFullCatalogSizedBatch(t *testing.T) {
	r, db := newModelTestRouter(t)
	p := seedProviderRow(t, db, "prov-prices")
	names := make([]string, 501)
	for i := range names {
		names[i] = fmt.Sprintf("vendor/model-%d", i)
	}
	w, _ := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/admin/providers/%d/models/suggest-prices", p.ID),
		map[string]interface{}{"names": names}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for a 501-name price look-up, got %d, body: %s", w.Code, w.Body.String()[:200])
	}
}
