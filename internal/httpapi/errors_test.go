package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidationErrorFormat(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/v1/users/me", nil)
	w := httptest.NewRecorder()
	writeValidationError(w, r, []fieldError{
		{Field: "display_name", Code: fieldTooLong, Detail: "must be at most 64 characters"},
	})

	p := assertError(t, w, http.StatusBadRequest, codeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0] != (fieldError{
		Field: "display_name", Code: fieldTooLong, Detail: "must be at most 64 characters",
	}) {
		t.Errorf("errors = %+v", p.Errors)
	}
	// The exact wire format, as documented in the README.
	want := `{"type":"about:blank","title":"Bad Request","status":400,"detail":"the request has invalid fields",` +
		`"instance":"/v1/users/me","code":"validation_failed",` +
		`"errors":[{"field":"display_name","code":"too_long","detail":"must be at most 64 characters"}]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestOtherErrorsHaveNoFieldList(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, httptest.NewRequest(http.MethodGet, "/v1/users/me", nil), http.StatusNotFound, codeProfileNotFound, "x")
	if strings.Contains(w.Body.String(), `"errors"`) {
		t.Errorf("body has an errors member: %s", w.Body.String())
	}
}
