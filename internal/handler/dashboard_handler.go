// Package handler exposes the dashboard endpoint.
// Thin HTTP adapter over DashboardService — all composition lives in the
// service, all SQL lives in the repository.
package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/service"
	"github.com/yolorouter/yolorouter/pkg/errcode"
	"github.com/yolorouter/yolorouter/pkg/response"
)

// GetDashboard handles GET /api/admin/dashboard. The dashboard is read-only
// apart from an optional ?timezone= query param carrying the admin's IANA
// timezone (e.g. "Asia/Shanghai") from the browser's
// Intl.DateTimeFormat().resolvedOptions().timeZone. Used for day-bucket
// windowing so the dashboard's "today" matches the admin's wall clock;
// missing or invalid values fall back to the server's local zone. Every
// other windowing / limit constant is pinned and lives in the service, so
// this handler only translates the service error into the project's response
// envelope.
func GetDashboard(svc *service.DashboardService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var loc *time.Location
		if v, ok := c.Get("timezone"); ok {
			loc = v.(*time.Location)
		}
		data, err := svc.GetDashboard(loc)
		if err != nil {
			response.Error(c, errcode.InternalError, errcode.GetMessage(errcode.InternalError))
			return
		}
		response.Success(c, data)
	}
}
