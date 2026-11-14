package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/distrocommerce/profile-service/internal/store"
)

// readinessPingTimeout stays below the kubelet probe timeout (1 s by default, and service-lib
// sets none), so a slow database gives a 503 from here instead of a probe timeout.
const readinessPingTimeout = 800 * time.Millisecond

func (a *API) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) readyz(w http.ResponseWriter, r *http.Request) {
	if a.draining.Load() {
		writeError(w, r, http.StatusServiceUnavailable, codeShuttingDown, "the server is shutting down")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readinessPingTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		a.log.WarnContext(r.Context(), "readiness check failed", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeDatabaseUnavailable, "the database does not answer")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// requireUser returns the caller's user ID, or writes the 401 response and
// reports false.
func requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := userID(r)
	if id == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, r, http.StatusUnauthorized, codeUnauthenticated, "missing "+userIDHeader+" header")
		return "", false
	}
	return id, true
}

func (a *API) getMe(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUser(w, r)
	if !ok {
		return
	}
	p, err := a.store.GetProfile(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeProfileNotFound, "no profile exists for this user")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// putMeRequest is the body of PUT /v1/users/me: every client-writable field.
// DisplayName is a pointer so that a missing member (or null) is told apart
// from "".
type putMeRequest struct {
	DisplayName *string `json:"display_name"`
}

// putMe creates the caller's profile (201) or replaces its client-writable
// fields (200, also when nothing changes).
func (a *API) putMe(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUser(w, r)
	if !ok {
		return
	}
	// A pointer, so that a JSON null body (not an object) is caught.
	var req *putMeRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req == nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidBody, "the request body must be a JSON object")
		return
	}
	if req.DisplayName == nil {
		writeValidationError(w, r, []fieldError{{Field: "display_name", Code: fieldRequired, Detail: "is required"}})
		return
	}
	name, ferr := validateDisplayName(*req.DisplayName)
	if ferr != nil {
		writeValidationError(w, r, []fieldError{*ferr})
		return
	}
	p, created, err := a.store.UpsertProfile(r.Context(), id, name)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, p)
}

func (a *API) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.log.ErrorContext(r.Context(), "request failed", "error", err)
	writeError(w, r, http.StatusInternalServerError, codeInternal, "")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the status is sent; a write error means the client is gone
}

// decodeJSON reads a JSON request body into v. Only whitespace may follow the
// value. It reports false after writing the error response itself (413 when
// the body exceeds MAX_BODY_BYTES, 400 otherwise). Write endpoints use it so
// that the limit is enforced in one place.
func (a *API) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		err = expectEOF(dec)
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeTooLarge(w, r, tooLarge.Limit)
		return false
	}
	writeError(w, r, http.StatusBadRequest, codeInvalidBody, "the request body is not valid JSON for this endpoint")
	return false
}

var errTrailingData = errors.New("data after the JSON value")

// expectEOF reports an error unless only whitespace is left in dec's input.
// Token, not More: More reports false for a stray '}' or ']'.
func expectEOF(dec *json.Decoder) error {
	_, err := dec.Token()
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return err
	default:
		return errTrailingData
	}
}
