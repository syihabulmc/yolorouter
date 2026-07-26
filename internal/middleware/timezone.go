package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
)

// Timezone reads the browser-supplied timezone (an IANA zone name like
// "Asia/Shanghai") from the X-Timezone header, falling back to a ?timezone=
// query param for navigation-based requests (e.g. CSV export via <a>.click()
// that cannot carry custom headers). Stores the parsed *time.Location in the
// request context under "timezone". Missing or invalid values leave the key
// unset — callers fall back to time.Local.
func Timezone() gin.HandlerFunc {
	return func(c *gin.Context) {
		tz := c.GetHeader("X-Timezone")
		if tz == "" {
			tz = c.Query("timezone")
		}
		if tz != "" {
			if loc, err := time.LoadLocation(tz); err == nil {
				c.Set("timezone", loc)
			}
		}
		c.Next()
	}
}
