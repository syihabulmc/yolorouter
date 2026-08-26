package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/yolorouter/yolorouter/pkg/logger"
)

// AccessLog logs after the response is written (via a real defer, not just
// code placed after c.Next()) so it captures the final status code —
// including ones set by Recovery/error envelopes, and including the case
// where Recovery re-panics with http.ErrAbortHandler after a post-write
// panic (see recovery.go): that panic unwinds straight through a plain
// post-c.Next() statement without ever reaching it, silently losing the
// access log entry for exactly the requests most worth logging. It
// intentionally does not log Authorization/Cookie headers, request or
// response bodies, or query parameters.
//
// /healthz and HEAD requests are skipped to keep the access log useful on
// platforms that poll the healthcheck every few seconds (Railway, k8s, …) —
// on a quiet instance the healthcheck alone produces the bulk of access log
// lines, with no operational value, so they are filtered here rather than
// in the router (so any new healthcheck-style route added later does not
// silently re-introduce the noise).
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		defer func() {
			p := c.Request.URL.Path
			if p == "/healthz" || c.Request.Method == http.MethodHead {
				return
			}
			requestID, _ := c.Get(RequestIDKey)
			logger.Info("access",
				zap.String("request_id", fmt.Sprint(requestID)),
				zap.String("method", c.Request.Method),
				zap.String("path", p),
				zap.Int("status", c.Writer.Status()),
				zap.Int64("duration_ms", time.Since(start).Milliseconds()),
				zap.Int("bytes", c.Writer.Size()),
				zap.String("client_ip", c.ClientIP()),
			)
		}()
		c.Next()
	}
}
