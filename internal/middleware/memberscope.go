package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/pkg/errcode"
)

// ForcedUserIDKey is the gin.Context key MemberScope sets for non-admin
// sessions: the account id every ownership-scoped query MUST be pinned
// to, regardless of what the request's own parameters say. Handlers read
// it via ForcedUserID.
const ForcedUserIDKey = "forced_user_id"

// MemberScope marks non-admin sessions with their forced ownership
// scope. It must run after RequireSession (it reads the resolved role).
// Admins pass through unmarked — their requests keep the optional
// filter-by-anyone semantics. This is the enforcement half of the member
// data-visibility rule: hiding pages in the frontend is cosmetic, this
// is what actually pins a member's queries to their own rows.
func MemberScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, ok := c.Get(UserRoleKey)
		if !ok || role != model.RoleAdmin {
			// Fail closed exactly like RequireAdmin: a route wired with
			// MemberScope but no RequireSession has no user id to scope to
			// and must not proceed unscoped.
			userID, hasUser := c.Get(UserIDKey)
			if !hasUser {
				WriteAdminError(c, http.StatusForbidden, errcode.AccountPageForbidden)
				return
			}
			c.Set(ForcedUserIDKey, userID.(uint))
		}
		c.Next()
	}
}

// ForcedUserID returns the ownership scope MemberScope pinned for this
// request, or nil for an unrestricted (admin) session.
func ForcedUserID(c *gin.Context) *uint {
	v, ok := c.Get(ForcedUserIDKey)
	if !ok {
		return nil
	}
	id := v.(uint)
	return &id
}
