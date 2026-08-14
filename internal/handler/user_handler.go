// Package handler: user directory endpoints.
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/middleware"
	"github.com/yolorouter/yolorouter/internal/service"
	"github.com/yolorouter/yolorouter/pkg/errcode"
	"github.com/yolorouter/yolorouter/pkg/response"
)

// GetUsers handles GET /api/admin/users — the full account list for the
// admin's user directory and the "filter by user" dropdowns on the
// statistics pages. Small deployments (company-internal), so no pagination.
func GetUsers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		users, err := service.ListUsers(db)
		if err != nil {
			middleware.WriteAdminError(c, http.StatusInternalServerError, errcode.DatabaseError)
			return
		}
		response.Success(c, gin.H{"users": users})
	}
}
