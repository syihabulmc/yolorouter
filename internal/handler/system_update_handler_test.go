package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/selfupdate"
	"github.com/yolorouter/yolorouter/pkg/errcode"
)

// fakeUpdate is a test double for the apply + restart dependencies: it
// records invocations and returns a scripted result, so the handler's
// gating and sequencing are testable without touching network or disk.
type fakeUpdate struct {
	result       selfupdate.Result
	err          error
	applyCalls   int
	restartCalls int
}

func (f *fakeUpdate) apply(_ context.Context) (selfupdate.Result, error) {
	f.applyCalls++
	return f.result, f.err
}

func (f *fakeUpdate) restart() { f.restartCalls++ }

func newUpdateTestRouter(mode string, fake *fakeUpdate) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/admin/system/update", PostSystemUpdate(mode, fake.apply, fake.restart))
	return r
}

func postUpdate(t *testing.T, r *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/system/update", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPostSystemUpdateAppliesAndRestarts(t *testing.T) {
	fake := &fakeUpdate{result: selfupdate.Result{Target: "v9.9.9", BackupPath: "/x/yolorouter.bak"}}
	r := newUpdateTestRouter(selfupdate.ModeInPlace, fake)

	w := postUpdate(t, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body %s", w.Code, w.Body.String())
	}
	data := decodeEnvelopeData(t, w.Body.Bytes())
	assertField(t, data, "status", "updated")
	assertField(t, data, "target", "v9.9.9")
	if fake.applyCalls != 1 {
		t.Fatalf("apply called %d times, want 1", fake.applyCalls)
	}
	if fake.restartCalls != 1 {
		t.Fatalf("restart called %d times, want 1 — without it the new binary never starts serving", fake.restartCalls)
	}
}

func TestPostSystemUpdateUpToDateSkipsRestart(t *testing.T) {
	fake := &fakeUpdate{result: selfupdate.Result{Target: "v1.0.0", UpToDate: true}}
	r := newUpdateTestRouter(selfupdate.ModeInPlace, fake)

	w := postUpdate(t, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body %s", w.Code, w.Body.String())
	}
	data := decodeEnvelopeData(t, w.Body.Bytes())
	assertField(t, data, "status", "up_to_date")
	if fake.restartCalls != 0 {
		t.Fatalf("an up-to-date result must not restart the process (restart called %d times)", fake.restartCalls)
	}
}

func TestPostSystemUpdateRefusesEveryNonInPlaceMode(t *testing.T) {
	for _, mode := range []string{
		selfupdate.ModeContainer,
		selfupdate.ModeWindows,
		selfupdate.ModeDisabled,
		selfupdate.ModeDevBuild,
		selfupdate.ModeCapabilities,
	} {
		t.Run(mode, func(t *testing.T) {
			fake := &fakeUpdate{result: selfupdate.Result{Target: "v9.9.9"}}
			r := newUpdateTestRouter(mode, fake)

			w := postUpdate(t, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("mode %q: expected 400, got %d, body %s", mode, w.Code, w.Body.String())
			}
			var env struct {
				Code int `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if env.Code != errcode.SystemUpdateUnsupported {
				t.Fatalf("mode %q: envelope code = %d, want %d", mode, env.Code, errcode.SystemUpdateUnsupported)
			}
			// The refusal must happen before any update work: a container or
			// windows process must never even attempt a binary replacement.
			if fake.applyCalls != 0 || fake.restartCalls != 0 {
				t.Fatalf("mode %q: apply/restart ran (%d/%d), must both be 0", mode, fake.applyCalls, fake.restartCalls)
			}
		})
	}
}

// TestPostSystemUpdateSurvivesClientDisconnect pins the commitment-point
// contract: a caller whose connection is already gone (cancelled request
// context) must not abort the in-flight update — the apply must receive a
// context that is still alive.
func TestPostSystemUpdateSurvivesClientDisconnect(t *testing.T) {
	var applyCtxErr error
	gin.SetMode(gin.TestMode)
	r := gin.New()
	restarted := 0
	r.POST("/api/admin/system/update", PostSystemUpdate(selfupdate.ModeInPlace,
		func(ctx context.Context) (selfupdate.Result, error) {
			applyCtxErr = ctx.Err()
			return selfupdate.Result{Target: "v9.9.9"}, nil
		},
		func() { restarted++ },
	))

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/system/update", nil).WithContext(cancelled)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if applyCtxErr != nil {
		t.Fatalf("apply saw a dead context (%v); a dropped connection must not abort the update", applyCtxErr)
	}
	if restarted != 1 {
		t.Fatalf("restart called %d times, want 1 — the update completed and must still restart", restarted)
	}
}

// TestPostSystemUpdateGateBlocksSecondRunAfterSuccess pins the rollback-
// backup protection: once one update applied, every later POST (until the
// process restarts) must be refused without running apply — a second apply
// would overwrite <exe>.bak with the already-new binary.
func TestPostSystemUpdateGateBlocksSecondRunAfterSuccess(t *testing.T) {
	fake := &fakeUpdate{result: selfupdate.Result{Target: "v9.9.9"}}
	r := newUpdateTestRouter(selfupdate.ModeInPlace, fake)

	if w := postUpdate(t, r); w.Code != http.StatusOK {
		t.Fatalf("first update: expected 200, got %d", w.Code)
	}

	w := postUpdate(t, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("second update before restart: expected 409, got %d, body %s", w.Code, w.Body.String())
	}
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Code != errcode.SystemUpdateInProgress {
		t.Fatalf("envelope code = %d, want %d", env.Code, errcode.SystemUpdateInProgress)
	}
	if fake.applyCalls != 1 {
		t.Fatalf("apply ran %d times, want 1 — the second run would destroy the rollback backup", fake.applyCalls)
	}
}

// TestPostSystemUpdateGateReopensAfterFailure: a failed run replaced
// nothing, so the operator must be able to retry.
func TestPostSystemUpdateGateReopensAfterFailure(t *testing.T) {
	fake := &fakeUpdate{err: errors.New("download blew up")}
	r := newUpdateTestRouter(selfupdate.ModeInPlace, fake)

	if w := postUpdate(t, r); w.Code != http.StatusInternalServerError {
		t.Fatalf("failed update: expected 500, got %d", w.Code)
	}

	fake.err = nil
	fake.result = selfupdate.Result{Target: "v9.9.9"}
	if w := postUpdate(t, r); w.Code != http.StatusOK {
		t.Fatalf("retry after failure must be allowed, got %d, body %s", postUpdate(t, r).Code, w.Body.String())
	}
	if fake.applyCalls != 2 {
		t.Fatalf("apply ran %d times, want 2 (initial failure + successful retry)", fake.applyCalls)
	}
}

// TestPostSystemUpdateUnsupportedMessageCarriesGuidance pins the endpoint
// contract for bare API clients: the refusal message itself must say what
// to do in this runtime, not just that the runtime is unsupported.
func TestPostSystemUpdateUnsupportedMessageCarriesGuidance(t *testing.T) {
	cases := map[string]string{
		selfupdate.ModeContainer:    "pull the newer image",
		selfupdate.ModeWindows:      "download the latest release",
		selfupdate.ModeCapabilities: "file capabilities",
		selfupdate.ModeDisabled:     "disabled by configuration",
		selfupdate.ModeDevBuild:     "not a release build",
	}
	for mode, want := range cases {
		t.Run(mode, func(t *testing.T) {
			fake := &fakeUpdate{}
			r := newUpdateTestRouter(mode, fake)
			w := postUpdate(t, r)
			var env struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if !strings.Contains(env.Message, want) {
				t.Fatalf("mode %q refusal message must carry guidance containing %q, got %q", mode, want, env.Message)
			}
		})
	}
}

func TestPostSystemUpdateFailureReturns500AndNoRestart(t *testing.T) {
	fake := &fakeUpdate{err: errors.New("checksum verification failed, not replacing")}
	r := newUpdateTestRouter(selfupdate.ModeInPlace, fake)

	w := postUpdate(t, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d, body %s", w.Code, w.Body.String())
	}
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Code != errcode.SystemUpdateFailed {
		t.Fatalf("envelope code = %d, want %d", env.Code, errcode.SystemUpdateFailed)
	}
	// A failed apply must not restart: the old binary is still the one on
	// disk (or the replacement never completed), and bouncing the process
	// would just cause an outage without an upgrade.
	if fake.restartCalls != 0 {
		t.Fatalf("restart ran after a failed apply (%d times)", fake.restartCalls)
	}
}
