package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/distrocommerce/profile-service/internal/store"
)

const readinessPingTimeout = 2 * time.Second

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

func (a *API) getMe(w http.ResponseWriter, r *http.Request) {
	id := userID(r)
	if id == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, r, http.StatusUnauthorized, codeUnauthenticated, "missing "+userIDHeader+" header")
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

func (a *API) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.log.ErrorContext(r.Context(), "request failed", "error", err)
	writeError(w, r, http.StatusInternalServerError, codeInternal, "")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the status is sent; a write error means the client is gone
}

// decodeJSON reads a JSON request body into v. It reports false after writing
// the error response itself (413 when the body exceeds MAX_BODY_BYTES, 400
// otherwise). Write endpoints use it so that the limit is enforced in one place.
func (a *API) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
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
