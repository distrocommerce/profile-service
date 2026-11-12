package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/distrocommerce/profile-service/internal/store"
	"github.com/distrocommerce/profile-service/internal/telemetry"
)

type fakeStore struct {
	pingErr  error
	profiles map[string]store.Profile
	getErr   error
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func (f *fakeStore) GetProfile(_ context.Context, id string) (store.Profile, error) {
	if f.getErr != nil {
		return store.Profile{}, f.getErr
	}
	p, ok := f.profiles[id]
	if !ok {
		return store.Profile{}, store.ErrNotFound
	}
	return p, nil
}

const testMaxBody = 64

func newTestAPI(st Store, logOut io.Writer) *API {
	if logOut == nil {
		logOut = io.Discard
	}
	return New(st, telemetry.NewLogger(logOut, slog.LevelDebug), testMaxBody)
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// assertError checks an RFC 9457 response and returns it.
func assertError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) errorResponse {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body %q)", w.Code, status, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != errorContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, errorContentType)
	}
	var p errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, w.Body.String())
	}
	if p.Status != status || p.Code != code || p.Type != "about:blank" || p.Title != http.StatusText(status) {
		t.Fatalf("error response = %+v, want status %d code %q", p, status, code)
	}
	return p
}

func TestHealthz(t *testing.T) {
	// Liveness must not depend on the database.
	h := newTestAPI(&fakeStore{pingErr: errors.New("down")}, nil).Handler()
	w := serve(h, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok"`) {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestReadyz(t *testing.T) {
	t.Run("database up", func(t *testing.T) {
		w := serve(newTestAPI(&fakeStore{}, nil).Handler(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("database down", func(t *testing.T) {
		w := serve(newTestAPI(&fakeStore{pingErr: errors.New("connection refused")}, nil).Handler(),
			httptest.NewRequest(http.MethodGet, "/readyz", nil))
		p := assertError(t, w, http.StatusServiceUnavailable, codeDatabaseUnavailable)
		if strings.Contains(p.Detail, "refused") {
			t.Errorf("detail leaks the internal error: %q", p.Detail)
		}
	})
	t.Run("draining", func(t *testing.T) {
		api := newTestAPI(&fakeStore{}, nil)
		api.StartDraining()
		w := serve(api.Handler(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
		assertError(t, w, http.StatusServiceUnavailable, codeShuttingDown)
	})
}

func TestGetMe(t *testing.T) {
	created := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st := &fakeStore{profiles: map[string]store.Profile{
		"312345678901234567": {UserID: "312345678901234567", DisplayName: "Ada", Email: "ada@example.com",
			CreatedAt: created, UpdatedAt: created},
	}}

	t.Run("found", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
		r.Header.Set("X-User-ID", "312345678901234567")
		w := serve(newTestAPI(st, nil).Handler(), r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("got %d %q", w.Code, w.Header().Get("Content-Type"))
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"user_id": "312345678901234567", "display_name": "Ada", "email": "ada@example.com",
			"created_at": "2026-11-01T12:00:00Z", "updated_at": "2026-11-01T12:00:00Z",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %v, want %v", k, got[k], v)
			}
		}
	})

	t.Run("no profile row", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
		r.Header.Set("X-User-ID", "999")
		p := assertError(t, serve(newTestAPI(st, nil).Handler(), r), http.StatusNotFound, codeProfileNotFound)
		if p.Instance != "/v1/users/me" {
			t.Errorf("instance = %q", p.Instance)
		}
	})

	for name, header := range map[string]string{"missing header": "", "blank header": "   "} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
			if header != "" {
				r.Header.Set("X-User-ID", header)
			}
			w := serve(newTestAPI(st, nil).Handler(), r)
			assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
			if w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
			}
		})
	}

	t.Run("store error", func(t *testing.T) {
		var logs bytes.Buffer
		r := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
		r.Header.Set("X-User-ID", "1")
		w := serve(newTestAPI(&fakeStore{getErr: errors.New("pq: secret detail")}, &logs).Handler(), r)
		p := assertError(t, w, http.StatusInternalServerError, codeInternal)
		if strings.Contains(p.Detail, "secret") {
			t.Errorf("detail leaks the internal error: %q", p.Detail)
		}
		if !strings.Contains(logs.String(), "pq: secret detail") {
			t.Errorf("error not logged: %s", logs.String())
		}
	})
}

func TestRouterErrors(t *testing.T) {
	h := newTestAPI(&fakeStore{}, nil).Handler()

	t.Run("unknown path", func(t *testing.T) {
		assertError(t, serve(h, httptest.NewRequest(http.MethodGet, "/nope", nil)), http.StatusNotFound, codeNotFound)
	})
	t.Run("wrong method", func(t *testing.T) {
		w := serve(h, httptest.NewRequest(http.MethodPost, "/v1/users/me", nil))
		assertError(t, w, http.StatusMethodNotAllowed, codeMethodNotAllowed)
		if allow := w.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
		}
	})
}

func TestBodyLimit(t *testing.T) {
	api := newTestAPI(&fakeStore{}, nil)

	t.Run("declared length over the limit", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/users/me", strings.NewReader(strings.Repeat("x", testMaxBody+1)))
		r.Header.Set("X-User-ID", "1")
		assertError(t, serve(api.Handler(), r), http.StatusRequestEntityTooLarge, codeRequestTooLarge)
	})

	// A body without Content-Length (chunked) is cut by http.MaxBytesReader
	// and reported by decodeJSON, which write endpoints use.
	decode := api.limitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if api.decodeJSON(w, r, &v) {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	newReq := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.ContentLength = -1 // unknown length
		return r
	}

	t.Run("streamed body over the limit", func(t *testing.T) {
		body := `{"a":"` + strings.Repeat("x", testMaxBody) + `"}`
		assertError(t, serve(decode, newReq(body)), http.StatusRequestEntityTooLarge, codeRequestTooLarge)
	})
	t.Run("body within the limit", func(t *testing.T) {
		if w := serve(decode, newReq(`{"a":"b"}`)); w.Code != http.StatusNoContent {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("invalid JSON", func(t *testing.T) {
		assertError(t, serve(decode, newReq(`{`)), http.StatusBadRequest, codeInvalidBody)
	})
}

func TestPanicRecovery(t *testing.T) {
	var logs bytes.Buffer
	api := newTestAPI(&fakeStore{}, &logs)

	t.Run("panic before writing", func(t *testing.T) {
		h := api.accessLog(api.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		})))
		assertError(t, serve(h, httptest.NewRequest(http.MethodGet, "/x", nil)), http.StatusInternalServerError, codeInternal)
		if !strings.Contains(logs.String(), `"panic":"boom"`) || !strings.Contains(logs.String(), `"status":500`) {
			t.Errorf("panic or status not logged: %s", logs.String())
		}
	})
	t.Run("abort handler is re-raised", func(t *testing.T) {
		h := api.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))
		defer func() {
			if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // sentinel
				t.Errorf("recovered %v, want http.ErrAbortHandler", v)
			}
		}()
		serve(h, httptest.NewRequest(http.MethodGet, "/x", nil))
	})
}

// TestTracingAndAccessLog runs through the real chain with an in-memory
// tracer: API requests get a span named after the route and a trace_id in the
// access log; probes are not traced.
func TestTracingAndAccessLog(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	var logs bytes.Buffer
	h := newTestAPI(&fakeStore{}, &logs).Handler()

	serve(h, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	serve(h, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	r := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
	r.Header.Set("X-User-ID", "42")
	serve(h, r)

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1 (probes must not be traced)", len(spans))
	}
	if name := spans[0].Name(); name != "GET /v1/users/me" {
		t.Errorf("span name = %q", name)
	}
	traceID := spans[0].SpanContext().TraceID().String()

	var line map[string]any
	for _, l := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err == nil && m["msg"] == "request" && m["path"] == "/v1/users/me" {
			line = m
		}
	}
	if line == nil {
		t.Fatalf("no access log line for /v1/users/me in:\n%s", logs.String())
	}
	if line["method"] != "GET" || line["status"] != float64(404) || line["route"] != "GET /v1/users/me" ||
		line["trace_id"] != traceID || line["duration_ms"] == nil {
		t.Errorf("access log line = %v (trace %s)", line, traceID)
	}
}
