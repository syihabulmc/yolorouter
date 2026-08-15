package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/model"
)

// memberScopeProbe runs one request through MemberScope with the given
// context pre-population (simulating what RequireSession would have set)
// and reports the response code plus the ForcedUserID the handler saw.
func memberScopeProbe(t *testing.T, populate func(*gin.Context)) (int, *uint, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var forced *uint
	handlerRan := false
	r.GET("/probe", func(c *gin.Context) {
		if populate != nil {
			populate(c)
		}
		c.Next()
	}, MemberScope(), func(c *gin.Context) {
		handlerRan = true
		forced = ForcedUserID(c)
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	return w.Code, forced, handlerRan
}

// TestMemberScopeFailsClosedWithoutSession: wired without RequireSession
// there is no user id to scope to — the request must be rejected, not let
// through unscoped.
func TestMemberScopeFailsClosedWithoutSession(t *testing.T) {
	code, _, handlerRan := memberScopeProbe(t, nil)
	if code != http.StatusForbidden {
		t.Fatalf("MemberScope without a session must answer 403, got %d", code)
	}
	if handlerRan {
		t.Fatalf("MemberScope without a session must abort the chain, but the handler ran")
	}
}

// TestMemberScopePinsMemberAndPassesAdmin: a member session gets the
// forced-scope mark with their own id; an admin session passes unmarked.
func TestMemberScopePinsMemberAndPassesAdmin(t *testing.T) {
	code, forced, _ := memberScopeProbe(t, func(c *gin.Context) {
		c.Set(UserIDKey, uint(42))
		c.Set(UserRoleKey, model.RoleMember)
	})
	if code != http.StatusOK || forced == nil || *forced != 42 {
		t.Fatalf("member session: expected 200 with forced scope 42, got %d / %v", code, forced)
	}

	code, forced, _ = memberScopeProbe(t, func(c *gin.Context) {
		c.Set(UserIDKey, uint(1))
		c.Set(UserRoleKey, model.RoleAdmin)
	})
	if code != http.StatusOK || forced != nil {
		t.Fatalf("admin session: expected 200 with no forced scope, got %d / %v", code, forced)
	}
}
