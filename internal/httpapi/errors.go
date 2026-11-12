package httpapi

import (
	"encoding/json"
	"net/http"
)

// errorContentType is the media type of every error response (RFC 9457).
const errorContentType = "application/problem+json"

// Stable error codes: the `code` member of error responses, for clients to switch on.
const (
	codeUnauthenticated     = "unauthenticated"
	codeProfileNotFound     = "profile_not_found"
	codeNotFound            = "not_found"
	codeMethodNotAllowed    = "method_not_allowed"
	codeRequestTooLarge     = "request_too_large"
	codeInvalidBody         = "invalid_body"
	codeInternal            = "internal_error"
	codeShuttingDown        = "shutting_down"
	codeDatabaseUnavailable = "database_unavailable"
)

// errorResponse is an RFC 9457 problem details object with one extension
// member, Code. Type stays "about:blank", so Title is the HTTP status phrase
// (RFC 9457, section 4.2.1).
type errorResponse struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code"`
}

// writeError sends an error response in the single error format of the
// service. Instance is the request path.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	body := errorResponse{
		Type:     "about:blank",
		Title:    http.StatusText(status),
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
		Code:     code,
	}
	h := w.Header()
	h.Set("Content-Type", errorContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Del("Content-Length")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // the status is sent; a write error means the client is gone
}
