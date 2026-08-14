package middleware

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/pkg/errcode"
)

// UserIDKey and UserRoleKey are the gin.Context keys RequireSession sets
// on success — handlers read them via c.MustGet(UserIDKey).(uint) /
// c.MustGet(UserRoleKey).(string).
const (
	UserIDKey   = "user_id"
	UserRoleKey = "user_role"
)

// SessionCookieName is the single cookie name used for the whole
// session lifecycle (set on login/setup, cleared on logout/password
// change). Exported so internal/handler (which writes
// and clears the cookie) and this package (which reads it) share one
// symbol instead of each hardcoding the literal.
const SessionCookieName = "session_id"

// RequireSession gates a route behind a valid, unexpired session cookie
// belonging to an enabled account. On any failure it writes the unified
// error envelope via WriteAdminError and aborts — it never lets a
// request without a resolved user identity reach the next handler.
//
// A disabled account with a still-live session gets an explicit 403
// account-disabled response rather than the generic 401: disabling a
// user must take effect immediately for their existing logins, and the
// holder of the session is the account's owner, so naming the reason
// leaks nothing.
func RequireSession(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(SessionCookieName)
		if err != nil || token == "" {
			WriteAdminError(c, http.StatusUnauthorized, errcode.AccountSessionInvalid)
			return
		}

		user, err := repository.FindUserByValidSession(db, token, time.Now().UTC())
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteAdminError(c, http.StatusUnauthorized, errcode.AccountSessionInvalid)
			return
		}
		if err != nil {
			WriteAdminError(c, http.StatusInternalServerError, errcode.DatabaseError)
			return
		}
		if user.Status != model.UserStatusEnabled {
			WriteAdminError(c, http.StatusForbidden, errcode.AccountDisabled)
			return
		}

		c.Set(UserIDKey, user.ID)
		c.Set(UserRoleKey, user.Role)
		c.Next()
	}
}

// RequireAdmin gates a route behind the admin role. It must run after
// RequireSession (it reads the role that middleware resolved); a route
// wired with RequireAdmin but no RequireSession fails closed on the
// missing context key rather than letting anonymous traffic through.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, ok := c.Get(UserRoleKey)
		if !ok || role != model.RoleAdmin {
			WriteAdminError(c, http.StatusForbidden, errcode.AccountPageForbidden)
			return
		}
		c.Next()
	}
}
