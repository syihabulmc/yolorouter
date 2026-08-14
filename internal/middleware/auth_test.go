package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

func seedUserWithSession(t *testing.T, db *gorm.DB, username, role, token string, status int) *model.User {
	t.Helper()
	now := time.Now().UTC()
	user := &model.User{
		Username:  username,
		Role:      role,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repository.CreateUser(db, user); err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if err := repository.CreateSession(db, token, user.ID, now.Add(time.Hour), now); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return user
}

func TestRequireSessionRejectsMissingCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.NewSQLiteDB(t)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/protected", RequireSession(db), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no cookie, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestRequireSessionRejectsUnknownSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.NewSQLiteDB(t)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/protected", RequireSession(db), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "no-such-token"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown session, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestRequireSessionAcceptsValidSessionAndSetsIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.NewSQLiteDB(t)
	user := seedUserWithSession(t, db, "alice", model.RoleAdmin, "valid-tok", model.UserStatusEnabled)

	r := gin.New()
	r.Use(RequestID())
	var gotUserID uint
	var gotRole string
	r.GET("/protected", RequireSession(db), func(c *gin.Context) {
		gotUserID = c.MustGet(UserIDKey).(uint)
		gotRole = c.MustGet(UserRoleKey).(string)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "valid-tok"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid session, got %d, body: %s", w.Code, w.Body.String())
	}
	if gotUserID != user.ID {
		t.Fatalf("expected user id %d in context, got %d", user.ID, gotUserID)
	}
	if gotRole != model.RoleAdmin {
		t.Fatalf("expected role %q in context, got %q", model.RoleAdmin, gotRole)
	}
}

func TestRequireSessionRejectsExpiredSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC()
	user := &model.User{Username: "bob", Role: model.RoleAdmin, Status: model.UserStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateUser(db, user); err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if err := repository.CreateSession(db, "expired-tok", user.ID, now.Add(-time.Minute), now.Add(-time.Hour)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	r := gin.New()
	r.Use(RequestID())
	r.GET("/protected", RequireSession(db), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "expired-tok"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired session, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestRequireSessionRejectsDisabledUser pins the immediate-lockout
// property of disabling an account: a still-live session must stop
// working the moment the user is disabled, with an explicit 403 (the
// session itself is valid — collapsing this into the 401 would tell the
// account's owner nothing). Remove the status check in RequireSession
// and this goes red.
func TestRequireSessionRejectsDisabledUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.NewSQLiteDB(t)
	seedUserWithSession(t, db, "muted", model.RoleMember, "disabled-tok", model.UserStatusDisabled)

	r := gin.New()
	r.Use(RequestID())
	r.GET("/protected", RequireSession(db), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "disabled-tok"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disabled user's session, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestRequireAdminRejectsMemberRole is the role gate itself: a member
// with a perfectly valid session must not pass RequireAdmin. Remove the
// role comparison and this goes red.
func TestRequireAdminRejectsMemberRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.NewSQLiteDB(t)
	seedUserWithSession(t, db, "worker", model.RoleMember, "member-tok", model.UserStatusEnabled)

	r := gin.New()
	r.Use(RequestID())
	r.GET("/admin-only", RequireSession(db), RequireAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/admin-only", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "member-tok"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for member on admin-only route, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestRequireAdminAcceptsAdminRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.NewSQLiteDB(t)
	seedUserWithSession(t, db, "boss", model.RoleAdmin, "admin-tok", model.UserStatusEnabled)

	r := gin.New()
	r.Use(RequestID())
	r.GET("/admin-only", RequireSession(db), RequireAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/admin-only", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "admin-tok"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin on admin-only route, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestRequireAdminFailsClosedWithoutRequireSession pins the wiring-mistake
// behavior: RequireAdmin on a route that forgot RequireSession must
// reject anonymous traffic (no context role → 403), never fall through.
func TestRequireAdminFailsClosedWithoutRequireSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/misconfigured", RequireAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/misconfigured", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when RequireSession is missing, got %d, body: %s", w.Code, w.Body.String())
	}
}
