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
	pingErr     error
	profiles    map[string]store.Profile
	getErr      error
	upsertErr   error
	upsertCalls int
	clock       time.Time // advanced one second per upsert
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

// UpsertProfile mimics the SQL statement: create, replace, or leave the row
// (and updated_at) alone when nothing changes.
func (f *fakeStore) UpsertProfile(_ context.Context, id, name string) (store.Profile, bool, error) {
	f.upsertCalls++
	if f.upsertErr != nil {
		return store.Profile{}, false, f.upsertErr
	}
	if f.clock.IsZero() {
		f.clock = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	}
	f.clock = f.clock.Add(time.Second)
	if f.profiles == nil {
		f.profiles = map[string]store.Profile{}
	}
	p, ok := f.profiles[id]
	if !ok {
		p = store.Profile{UserID: id, DisplayName: name, CreatedAt: f.clock, UpdatedAt: f.clock}
		f.profiles[id] = p
		return p, true, nil
	}
	if p.DisplayName != name {
		p.DisplayName, p.UpdatedAt = name, f.clock
		f.profiles[id] = p
	}
	return p, false, nil
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
		if allow := w.Header().Get("Allow"); allow != "GET, HEAD, PUT" {
			t.Errorf("Allow = %q, want %q", allow, "GET, HEAD, PUT")
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
	for name, body := range map[string]string{
		"trailing garbage": `{"a":"b"} garbage`,
		"second value":     `{"a":"b"}{"c":"d"}`,
		"stray brace":      `{"a":"b"}}`,
		"trailing number":  `{"a":"b"} 1`,
	} {
		t.Run(name, func(t *testing.T) {
			assertError(t, serve(decode, newReq(body)), http.StatusBadRequest, codeInvalidBody)
		})
	}
	t.Run("trailing whitespace", func(t *testing.T) {
		if w := serve(decode, newReq("{\"a\":\"b\"} \r\n\t \n")); w.Code != http.StatusNoContent {
			t.Fatalf("status = %d (body %q)", w.Code, w.Body.String())
		}
	})
	t.Run("trailing whitespace over the limit", func(t *testing.T) {
		body := `{"a":"b"}` + strings.Repeat(" ", testMaxBody)
		assertError(t, serve(decode, newReq(body)), http.StatusRequestEntityTooLarge, codeRequestTooLarge)
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

func newPutMe(userID, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/v1/users/me", strings.NewReader(body))
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	return r
}

// decodeProfile checks a profile response and returns it.
func decodeProfile(t *testing.T, w *httptest.ResponseRecorder, status int) store.Profile {
	t.Helper()
	if w.Code != status || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("got %d %q (body %q), want %d application/json", w.Code, w.Header().Get("Content-Type"), w.Body.String(), status)
	}
	var p store.Profile
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPutMe(t *testing.T) {
	const user = "312345678901234567"
	// A limit large enough for the longest valid names; TestPutMeBodyLimit
	// covers the limit itself.
	newAPI := func(st *fakeStore, logOut io.Writer) http.Handler {
		if logOut == nil {
			logOut = io.Discard
		}
		return New(st, telemetry.NewLogger(logOut, slog.LevelDebug), 4096).Handler()
	}

	t.Run("create returns 201 in the shape of GET", func(t *testing.T) {
		st := &fakeStore{}
		h := newAPI(st, nil)
		w := serve(h, newPutMe(user, `{"display_name":"Ada"}`))
		var got map[string]any
		_ = decodeProfile(t, w, http.StatusCreated)
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 || got["user_id"] != user || got["display_name"] != "Ada" || got["email"] != "" ||
			got["created_at"] == nil || got["created_at"] != got["updated_at"] {
			t.Errorf("body = %v", got)
		}

		r := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
		r.Header.Set("X-User-ID", user)
		if g := serve(h, r); g.Code != http.StatusOK || g.Body.String() != w.Body.String() {
			t.Errorf("GET after PUT = %d %q, want 200 %q", g.Code, g.Body.String(), w.Body.String())
		}
	})

	t.Run("updates", func(t *testing.T) {
		h := newAPI(&fakeStore{}, nil)
		first := decodeProfile(t, serve(h, newPutMe(user, `{"display_name":"Ada"}`)), http.StatusCreated)

		same := decodeProfile(t, serve(h, newPutMe(user, `{"display_name":"Ada"}`)), http.StatusOK)
		if same != first {
			t.Errorf("identical PUT = %+v, want unchanged %+v", same, first)
		}
		padded := decodeProfile(t, serve(h, newPutMe(user, `{"display_name":"  Ada "}`)), http.StatusOK)
		if padded != first {
			t.Errorf("PUT differing only in whitespace = %+v, want unchanged %+v", padded, first)
		}

		changed := decodeProfile(t, serve(h, newPutMe(user, `{"display_name":"Ada Lovelace"}`)), http.StatusOK)
		if changed.DisplayName != "Ada Lovelace" || !changed.CreatedAt.Equal(first.CreatedAt) ||
			!changed.UpdatedAt.After(first.UpdatedAt) {
			t.Errorf("changed PUT = %+v, first %+v", changed, first)
		}

		cleared := decodeProfile(t, serve(h, newPutMe(user, `{"display_name":""}`)), http.StatusOK)
		if cleared.DisplayName != "" || !cleared.UpdatedAt.After(changed.UpdatedAt) {
			t.Errorf("clearing PUT = %+v, before %+v", cleared, changed)
		}
	})

	existing := func() *fakeStore {
		return &fakeStore{profiles: map[string]store.Profile{user: {UserID: user, DisplayName: "Ada"}}}
	}
	assertUnchanged := func(t *testing.T, st *fakeStore) {
		t.Helper()
		if st.upsertCalls != 0 || st.profiles[user].DisplayName != "Ada" {
			t.Errorf("store touched: %d upserts, profile %+v", st.upsertCalls, st.profiles[user])
		}
	}

	for name, body := range map[string]string{
		"malformed JSON":        `{"display_name":`,
		"empty body":            ``,
		"wrong type":            `{"display_name":42}`,
		"array body":            `["Ada"]`,
		"null body":             `null`,
		"unknown field":         `{"display_name":"Ada","nickname":"A"}`,
		"email field":           `{"display_name":"Ada","email":"ada@example.com"}`,
		"user_id field":         `{"display_name":"Ada","user_id":"other"}`,
		"trailing data":         `{"display_name":"Ada"} garbage`,
		"second value":          `{"display_name":"Ada"}{"email":"ada@example.com"}`,
		"Email in another case": `{"display_name":"Ada","Email":"ada@example.com"}`,
	} {
		t.Run("invalid_body: "+name, func(t *testing.T) {
			st := existing()
			p := assertError(t, serve(newAPI(st, nil), newPutMe(user, body)), http.StatusBadRequest, codeInvalidBody)
			if p.Errors != nil {
				t.Errorf("errors = %+v, want none", p.Errors)
			}
			assertUnchanged(t, st)
		})
	}

	for name, tc := range map[string]struct{ body, code string }{
		"empty object":      {`{}`, fieldRequired},
		"null display_name": {`{"display_name":null}`, fieldRequired},
		"too long":          {`{"display_name":"` + strings.Repeat("é", 65) + `"}`, fieldTooLong},
		"control character": {`{"display_name":"Ada\nLovelace"}`, fieldInvalidCharacter},
		"NUL":               {`{"display_name":"Ada\u0000"}`, fieldInvalidCharacter},
	} {
		t.Run("validation_failed: "+name, func(t *testing.T) {
			st := existing()
			p := assertError(t, serve(newAPI(st, nil), newPutMe(user, tc.body)), http.StatusBadRequest, codeValidationFailed)
			if len(p.Errors) != 1 || p.Errors[0].Field != "display_name" || p.Errors[0].Code != tc.code || p.Errors[0].Detail == "" {
				t.Errorf("errors = %+v, want one display_name %s", p.Errors, tc.code)
			}
			if p.Instance != "/v1/users/me" {
				t.Errorf("instance = %q", p.Instance)
			}
			assertUnchanged(t, st)
		})
	}

	t.Run("header value is trimmed", func(t *testing.T) {
		st := &fakeStore{}
		p := decodeProfile(t, serve(newAPI(st, nil), newPutMe("  42  ", `{"display_name":"Ada"}`)), http.StatusCreated)
		if _, ok := st.profiles["42"]; p.UserID != "42" || !ok || len(st.profiles) != 1 {
			t.Errorf("user_id = %q, stored %v", p.UserID, st.profiles)
		}
	})

	t.Run("user_id in the query is ignored", func(t *testing.T) {
		st := &fakeStore{}
		r := httptest.NewRequest(http.MethodPut, "/v1/users/me?user_id=other", strings.NewReader(`{"display_name":"Ada"}`))
		r.Header.Set("X-User-ID", user)
		p := decodeProfile(t, serve(newAPI(st, nil), r), http.StatusCreated)
		if _, other := st.profiles["other"]; p.UserID != user || other || len(st.profiles) != 1 {
			t.Errorf("user_id = %q, stored %v", p.UserID, st.profiles)
		}
	})

	// Accepted encoding/json v1 behavior (see design.md); these fail once
	// decodeJSON matches member names exactly.
	t.Run("member names match case-insensitively", func(t *testing.T) {
		p := decodeProfile(t, serve(newAPI(&fakeStore{}, nil), newPutMe(user, `{"DISPLAY_NAME":"Ada"}`)), http.StatusCreated)
		if p.DisplayName != "Ada" {
			t.Errorf("display_name = %q, want Ada", p.DisplayName)
		}
	})
	t.Run("last repeated member wins", func(t *testing.T) {
		p := decodeProfile(t, serve(newAPI(&fakeStore{}, nil), newPutMe(user, `{"display_name":"Ada","Display_Name":"Eve"}`)), http.StatusCreated)
		if p.DisplayName != "Eve" {
			t.Errorf("display_name = %q, want Eve", p.DisplayName)
		}
	})

	t.Run("trailing whitespace", func(t *testing.T) {
		decodeProfile(t, serve(newAPI(&fakeStore{}, nil), newPutMe(user, "{\"display_name\":\"Ada\"}  \n")), http.StatusCreated)
	})

	t.Run("longest name in a multi-byte script", func(t *testing.T) {
		name := strings.Repeat("😀", 64)
		p := decodeProfile(t, serve(newAPI(&fakeStore{}, nil), newPutMe(user, `{"display_name":"`+name+`"}`)), http.StatusCreated)
		if p.DisplayName != name {
			t.Errorf("display_name = %q", p.DisplayName)
		}
	})

	for name, header := range map[string]string{"missing header": "", "blank header": "   "} {
		t.Run("401: "+name, func(t *testing.T) {
			st := &fakeStore{}
			r := newPutMe("", `{"display_name":"Ada"}`)
			if header != "" {
				r.Header.Set("X-User-ID", header)
			}
			w := serve(newAPI(st, nil), r)
			assertError(t, w, http.StatusUnauthorized, codeUnauthenticated)
			if w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
			}
			if st.upsertCalls != 0 {
				t.Errorf("store called %d times", st.upsertCalls)
			}
		})
	}

	t.Run("store error", func(t *testing.T) {
		var logs bytes.Buffer
		w := serve(newAPI(&fakeStore{upsertErr: errors.New("pq: secret detail")}, &logs), newPutMe(user, `{"display_name":"Ada"}`))
		p := assertError(t, w, http.StatusInternalServerError, codeInternal)
		if strings.Contains(p.Detail, "secret") {
			t.Errorf("detail leaks the internal error: %q", p.Detail)
		}
		if !strings.Contains(logs.String(), "pq: secret detail") {
			t.Errorf("error not logged: %s", logs.String())
		}
	})
}

func TestPutMeBodyLimit(t *testing.T) {
	body := `{"display_name":"` + strings.Repeat("a", testMaxBody) + `"}`

	t.Run("declared length over the limit", func(t *testing.T) {
		st := &fakeStore{}
		assertError(t, serve(newTestAPI(st, nil).Handler(), newPutMe("1", body)), http.StatusRequestEntityTooLarge, codeRequestTooLarge)
		if st.upsertCalls != 0 {
			t.Errorf("store called %d times", st.upsertCalls)
		}
	})
	t.Run("declared length over the limit without the header", func(t *testing.T) {
		// The body limit is checked before authentication.
		w := serve(newTestAPI(&fakeStore{}, nil).Handler(), newPutMe("", body))
		assertError(t, w, http.StatusRequestEntityTooLarge, codeRequestTooLarge)
	})
	t.Run("streamed body over the limit", func(t *testing.T) {
		st := &fakeStore{}
		r := newPutMe("1", body)
		r.ContentLength = -1 // unknown length
		assertError(t, serve(newTestAPI(st, nil).Handler(), r), http.StatusRequestEntityTooLarge, codeRequestTooLarge)
		if st.upsertCalls != 0 {
			t.Errorf("store called %d times", st.upsertCalls)
		}
	})
}
