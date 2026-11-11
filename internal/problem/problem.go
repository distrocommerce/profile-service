// Package problem writes RFC 9457 problem details (application/problem+json),
// the single error format of the service.
package problem

import (
	"encoding/json"
	"net/http"
)

// ContentType is the media type of every error response.
const ContentType = "application/problem+json"

// Details is an RFC 9457 problem object with one extension member, Code: a
// stable, machine-readable identifier clients can switch on. Type stays
// "about:blank", so Title is the HTTP status phrase (RFC 9457, section 4.2.1).
type Details struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code"`
}

// Write sends a problem response. Instance is the request path.
func Write(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	p := Details{
		Type:     "about:blank",
		Title:    http.StatusText(status),
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
		Code:     code,
	}
	h := w.Header()
	h.Set("Content-Type", ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Del("Content-Length")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p) // the status is sent; a write error means the client is gone
}
