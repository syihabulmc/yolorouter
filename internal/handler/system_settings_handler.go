package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/service"
	"github.com/yolorouter/yolorouter/pkg/errcode"
	"github.com/yolorouter/yolorouter/pkg/response"
)

// customSystemPromptResponse is the handler-facing response DTO with explicit
// json tags. The neutral settings.CustomSystemPromptSetting DTO intentionally
// has no json tags; response.Success serializes whatever it is given, so this
// wrapper fixes the wire field names (enabled/text/version).
type customSystemPromptResponse struct {
	Enabled bool   `json:"enabled"`
	Text    string `json:"text"`
	Version int64  `json:"version"`
}

// putCustomSystemPromptRequest uses pointers so absent fields can be detected
// and rejected. A partial body must not silently clear the prompt.
type putCustomSystemPromptRequest struct {
	Enabled *bool   `json:"enabled"`
	Text    *string `json:"text"`
	Version *int64  `json:"version"`
}

// GetCustomSystemPrompt returns the authoritative global state (DB read,
// bypassing the cache) so the admin always sees the committed value.
func GetCustomSystemPrompt(svc *service.SystemSettingsService) gin.HandlerFunc {
	return func(c *gin.Context) {
		s, ver, err := svc.GetCustomSystemPrompt(c.Request.Context())
		if err != nil {
			response.InternalError(c, err.Error())
			return
		}
		response.Success(c, customSystemPromptResponse{Enabled: s.Enabled, Text: s.Text, Version: ver})
	}
}

// PutCustomSystemPrompt validates + CAS-updates the global state. version is
// required (optimistic lock); enabled/text must both be present (pointers) so
// a partial body can't silently clear the prompt. A CAS miss returns 409.
func PutCustomSystemPrompt(svc *service.SystemSettingsService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req putCustomSystemPromptRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.ParamError(c, err.Error())
			return
		}
		if req.Enabled == nil || req.Text == nil || req.Version == nil || *req.Version < 1 {
			response.ParamError(c, "enabled, text and version (>=1) are all required")
			return
		}
		s, ver, err := svc.UpdateCustomSystemPrompt(c.Request.Context(), *req.Version, *req.Enabled, *req.Text)
		if err != nil {
			switch {
			case errors.Is(err, errcode.ErrCustomSystemPromptConflict):
				// 409 is not produced by httpStatusForCode's range mapping; set it explicitly.
				response.ErrorStatus(c, http.StatusConflict, errcode.CustomSystemPromptConflict, errcode.GetMessage(errcode.CustomSystemPromptConflict))
			case errors.Is(err, errcode.ErrCustomSystemPromptTooLong):
				response.Error(c, errcode.CustomSystemPromptTooLong, errcode.GetMessage(errcode.CustomSystemPromptTooLong))
			case errors.Is(err, errcode.ErrCustomSystemPromptEmpty):
				response.Error(c, errcode.CustomSystemPromptEmpty, errcode.GetMessage(errcode.CustomSystemPromptEmpty))
			default:
				response.InternalError(c, err.Error())
			}
			return
		}
		response.Success(c, customSystemPromptResponse{Enabled: s.Enabled, Text: s.Text, Version: ver})
	}
}
