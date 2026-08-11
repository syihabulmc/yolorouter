package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/service"
	"github.com/yolorouter/yolorouter/pkg/response"
)

type createModelRequest struct {
	Name string `json:"name" binding:"required,max=100"`
}

type batchCreateModelsRequest struct {
	Names []string `json:"names" binding:"required,min=1,max=200,dive,max=100"`
}

type updateModelRequest struct {
	Name string `json:"name" binding:"required,max=100"`
}

type setModelStatusRequest struct {
	Enabled bool `json:"enabled"`
}

type createCandidateRequest struct {
	ProviderID        uint     `json:"provider_id" binding:"required"`
	ProviderModelName string   `json:"provider_model_name" binding:"max=200"`
	InputPrice        float64  `json:"input_price" binding:"min=0"`
	OutputPrice       float64  `json:"output_price" binding:"min=0"`
	CacheWritePrice   *float64 `json:"cache_write_price" binding:"omitempty,min=0"`
	CacheReadPrice    *float64 `json:"cache_read_price" binding:"omitempty,min=0"`
	MaxOutput         int      `json:"max_output" binding:"min=0"`
	ManagementStatus  int      `json:"management_status" binding:"omitempty,oneof=1 2"`
}

type updateCandidateRequest struct {
	ProviderModelName string   `json:"provider_model_name" binding:"max=200"`
	InputPrice        float64  `json:"input_price" binding:"min=0"`
	OutputPrice       float64  `json:"output_price" binding:"min=0"`
	CacheWritePrice   *float64 `json:"cache_write_price" binding:"omitempty,min=0"`
	CacheReadPrice    *float64 `json:"cache_read_price" binding:"omitempty,min=0"`
	MaxOutput         int      `json:"max_output" binding:"min=0"`
	// A pointer so an omitted field stays distinguishable from a request to
	// disable. As a plain int, any client PATCHing only prices would send the
	// zero value and silently take the candidate out of routing.
	ManagementStatus *int `json:"management_status" binding:"omitempty,oneof=1 2"`
}

type candidateReorderRequest struct {
	Direction string `json:"direction" binding:"required,oneof=up down"`
}

// parseModelAndCandidateIDs parses both the ":id" and ":candidateId" path
// segments, writing a 400 and returning ok=false on the first failure —
// mirrors provider_handler.go's parseProviderAndKeyIDs.
func parseModelAndCandidateIDs(c *gin.Context) (modelID, candidateID uint, ok bool) {
	modelID, ok = parseUintParam(c, "id")
	if !ok {
		return 0, 0, false
	}
	candidateID, ok = parseUintParam(c, "candidateId")
	if !ok {
		return 0, 0, false
	}
	return modelID, candidateID, true
}

func GetModels(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		list, err := svc.ListModels()
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, gin.H{"list": list})
	}
}

func PostModel(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createModelRequest
		if !bindJSON(c, &req) {
			return
		}
		view, err := svc.CreateModel(service.CreateModelInput{Name: req.Name}, timeNow())
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, view)
	}
}

// PostModelsBatch creates several models at once from a list of names,
// skipping (rather than failing on) names that are invalid or already exist —
// the response carries the created models plus a per-name skip summary.
func PostModelsBatch(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req batchCreateModelsRequest
		if !bindJSON(c, &req) {
			return
		}
		result, err := svc.CreateModelsBatch(req.Names, timeNow())
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, result)
	}
}

func GetModel(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseUintParam(c, "id")
		if !ok {
			return
		}
		detail, err := svc.GetModelDetail(id)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, detail)
	}
}

func PatchModel(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseUintParam(c, "id")
		if !ok {
			return
		}
		var req updateModelRequest
		if !bindJSON(c, &req) {
			return
		}
		view, err := svc.UpdateModelNameStatus(id, req.Name, timeNow())
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, view)
	}
}

func PatchModelStatus(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseUintParam(c, "id")
		if !ok {
			return
		}
		var req setModelStatusRequest
		if !bindJSON(c, &req) {
			return
		}
		if err := svc.SetModelStatus(id, req.Enabled, timeNow()); err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, nil)
	}
}

// GetCandidateSuggestPrice returns a price to pre-fill when adding a candidate
// for a provider+model, checking the provider's own history first then the
// built-in seed catalog. provider_id and provider_model_name come from query
// params because this is a look-up the candidate form fires on model-name
// select, not a candidate-scoped path. An empty Source means nothing matched
// and the form stays at its default.
func GetCandidateSuggestPrice(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		providerID, ok := requireUintQuery(c, "provider_id")
		if !ok {
			return
		}
		modelName := strings.TrimSpace(c.Query("provider_model_name"))
		if modelName == "" {
			response.ParamError(c, "provider_model_name is required")
			return
		}
		view, err := svc.SuggestCandidatePrice(providerID, modelName)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, view)
	}
}

func PostModelCandidate(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelID, ok := parseUintParam(c, "id")
		if !ok {
			return
		}
		var req createCandidateRequest
		if !bindJSON(c, &req) {
			return
		}
		view, err := svc.CreateModelCandidate(c.Request.Context(), modelID, service.CreateCandidateInput{
			ProviderID: req.ProviderID, ProviderModelName: req.ProviderModelName,
			InputPrice: req.InputPrice, OutputPrice: req.OutputPrice,
			CacheWritePrice: req.CacheWritePrice, CacheReadPrice: req.CacheReadPrice,
			MaxOutput: req.MaxOutput, ManagementStatus: req.ManagementStatus,
		}, timeNow())
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, view)
	}
}

func PatchModelCandidate(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, candidateID, ok := parseModelAndCandidateIDs(c)
		if !ok {
			return
		}
		var req updateCandidateRequest
		if !bindJSON(c, &req) {
			return
		}
		result, err := svc.UpdateModelCandidate(c.Request.Context(), candidateID, service.UpdateCandidateInput{
			ProviderModelName: req.ProviderModelName, InputPrice: req.InputPrice, OutputPrice: req.OutputPrice,
			CacheWritePrice: req.CacheWritePrice, CacheReadPrice: req.CacheReadPrice, MaxOutput: req.MaxOutput,
			ManagementStatus: req.ManagementStatus,
		}, timeNow())
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, result)
	}
}

func PatchModelCandidateOrder(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelID, candidateID, ok := parseModelAndCandidateIDs(c)
		if !ok {
			return
		}
		var req candidateReorderRequest
		if !bindJSON(c, &req) {
			return
		}
		if err := svc.ReorderModelCandidate(modelID, candidateID, req.Direction); err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, nil)
	}
}

func PatchModelCandidateStatus(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, candidateID, ok := parseModelAndCandidateIDs(c)
		if !ok {
			return
		}
		var req setModelStatusRequest
		if !bindJSON(c, &req) {
			return
		}
		if err := svc.SetCandidateStatus(candidateID, req.Enabled, timeNow()); err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, nil)
	}
}

// PostModelCandidateTest re-probes a stored candidate. It takes no test_type:
// one retest covers the basic mapping and both capabilities, so an operator
// never has to pick which probe to run.
func PostModelCandidateTest(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, candidateID, ok := parseModelAndCandidateIDs(c)
		if !ok {
			return
		}
		view, err := svc.RetestModelCandidate(c.Request.Context(), candidateID, timeNow())
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, view)
	}
}

// PostModelCandidateTestAndCreate probes a mapping and stores it only if the
// result allows what the caller asked for, so the admin UI can report the
// verdicts without a manual test step and without leaving a broken mapping
// behind when enablement was requested but the mapping does not work.
func PostModelCandidateTestAndCreate(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelID, ok := parseUintParam(c, "id")
		if !ok {
			return
		}
		var req createCandidateRequest
		if !bindJSON(c, &req) {
			return
		}
		result, err := svc.TestAndCreateCandidate(c.Request.Context(), modelID, service.CreateCandidateInput{
			ProviderID: req.ProviderID, ProviderModelName: req.ProviderModelName,
			InputPrice: req.InputPrice, OutputPrice: req.OutputPrice,
			CacheWritePrice: req.CacheWritePrice, CacheReadPrice: req.CacheReadPrice,
			MaxOutput: req.MaxOutput, ManagementStatus: req.ManagementStatus,
		}, timeNow())
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, result)
	}
}

func DeleteModelCandidate(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, candidateID, ok := parseModelAndCandidateIDs(c)
		if !ok {
			return
		}
		if err := svc.DeleteModelCandidate(candidateID); err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, nil)
	}
}

// GetModelImpact returns what disabling or renaming the model touches, for
// the confirm dialogs and the impact tab.
func GetModelImpact(svc *service.ModelService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseUintParam(c, "id")
		if !ok {
			return
		}
		view, err := svc.GetModelImpact(id, timeNow())
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, view)
	}
}
