package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/pkg/errcode"
)

// writeServiceErrorResponse drives writeServiceError against a bare test
// context and returns what would have gone to the wire.
func writeServiceErrorResponse(t *testing.T, err error) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	writeServiceError(c, err)
	return rec, rec.Body.String()
}

// TestConflictRowSetsExplicit409 pins the one row that overrides the HTTP
// status: the code's range mapping never produces 409, so dropping the
// override would silently demote a CAS conflict to whatever the range says —
// and the client-side retry logic keyed on 409 would stop firing.
func TestConflictRowSetsExplicit409(t *testing.T) {
	rec, body := writeServiceErrorResponse(t, errcode.ErrAPIKeyConflict)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409: the table's explicit override was lost", rec.Code)
	}
	if !strings.Contains(body, fmt.Sprint(errcode.APIKeyConflict)) {
		t.Errorf("body %q does not carry code %d", body, errcode.APIKeyConflict)
	}
}

// TestVerbatimRowsReturnTheWrappedText pins the rows marked safe to echo:
// their wrapped text names client-supplied input, and swallowing it behind
// the code's fixed message would tell an admin "invalid" without saying what
// was.
func TestVerbatimRowsReturnTheWrappedText(t *testing.T) {
	wrapped := fmt.Errorf("%w: invalid provider_type: bogus", errcode.ErrProviderProtocolInvalid)
	_, body := writeServiceErrorResponse(t, wrapped)
	if !strings.Contains(body, "invalid provider_type: bogus") {
		t.Errorf("body %q lost the verbatim validation detail", body)
	}
}

// TestUnmatchedErrorStaysGeneric pins the anti-leak default: an error no row
// matches must surface as the fixed generic message, never its own text,
// whose wrapped crypto/gorm detail is internal.
func TestUnmatchedErrorStaysGeneric(t *testing.T) {
	_, body := writeServiceErrorResponse(t, errors.New("pq: duplicate key value violates constraint api_keys_pkey"))
	if strings.Contains(body, "api_keys_pkey") {
		t.Errorf("body %q leaks internal error detail to the client", body)
	}
	if !strings.Contains(body, errcode.GetMessage(errcode.InternalError)) {
		t.Errorf("body %q does not carry the generic internal-error message", body)
	}
}
