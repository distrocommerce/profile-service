package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/distrocommerce/profile-service/internal/problem"
)

// problemRouter serves mux, but answers unknown paths (404) and wrong methods
// (405, keeping the mux's Allow header) in the problem format instead of the
// mux's plain text.
func problemRouter(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			// ServeHTTP (not h) so the mux sets r.Pattern and path values.
			mux.ServeHTTP(w, r)
			return
		}
		probe := &headerCapture{header: http.Header{}}
		h.ServeHTTP(probe, r)
		if probe.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", probe.header.Get("Allow"))
			problem.Write(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed,
				fmt.Sprintf("method %s is not allowed for this path", r.Method))
			return
		}
		problem.Write(w, r, http.StatusNotFound, codeNotFound, "no such endpoint")
	})
}

// headerCapture records the status and headers of the mux's fallback handler
// and discards its body.
type headerCapture struct {
	header http.Header
	status int
}

func (c *headerCapture) Header() http.Header         { return c.header }
func (c *headerCapture) Write(b []byte) (int, error) { return len(b), nil }
func (c *headerCapture) WriteHeader(status int)      { c.status = status }

// limitBody caps request bodies at maxBodyBytes: a declared Content-Length
// above the limit is rejected at once; otherwise http.MaxBytesReader stops the
// read and decodeJSON turns it into a 413.
func (a *API) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > a.maxBodyBytes {
			writeTooLarge(w, r, a.maxBodyBytes)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, a.maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func writeTooLarge(w http.ResponseWriter, r *http.Request, limit int64) {
	problem.Write(w, r, http.StatusRequestEntityTooLarge, codeRequestTooLarge,
		fmt.Sprintf("the request body exceeds %d bytes", limit))
}

// recoverPanics turns a panic into a logged error and a 500 problem response
// (if nothing was written yet). http.ErrAbortHandler is re-raised, as net/http
// expects.
func (a *API) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseRecorder{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared as net/http does
				panic(v)
			}
			a.log.ErrorContext(r.Context(), "panic", "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if !rw.wroteHeader {
				problem.Write(rw, r, http.StatusInternalServerError, codeInternal, "")
			}
		}()
		next.ServeHTTP(rw, r)
	})
}

// accessLog writes one line per request. Probe requests are logged at debug
// level so kubelet checks do not flood the log.
func (a *API) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rw, r)

		level := slog.LevelInfo
		if isProbe(r.URL.Path) {
			level = slog.LevelDebug
		}
		a.log.LogAttrs(r.Context(), level, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("route", r.Pattern),
			slog.Int("status", rw.statusCode()),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
		)
	})
}

// responseRecorder remembers the status code. Unwrap lets
// http.ResponseController reach the underlying writer (Flush, deadlines).
type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rw *responseRecorder) WriteHeader(status int) {
	if !rw.wroteHeader {
		rw.status = status
		rw.wroteHeader = true
	}
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseRecorder) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

func (rw *responseRecorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *responseRecorder) statusCode() int {
	if !rw.wroteHeader {
		return http.StatusOK
	}
	return rw.status
}
