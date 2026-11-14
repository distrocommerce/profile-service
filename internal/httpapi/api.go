// Package httpapi is the HTTP interface of the service: routes, handlers and
// the middleware chain.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/distrocommerce/profile-service/internal/store"
)

// Header set by Envoy from the validated token's `sub` (any client value is
// overwritten), so the service trusts it and does no JWT validation itself.
const userIDHeader = "X-User-ID"

// Store is the persistence the handlers need.
type Store interface {
	Ping(ctx context.Context) error
	GetProfile(ctx context.Context, userID string) (store.Profile, error)
	UpsertProfile(ctx context.Context, userID, displayName string) (store.Profile, bool, error)
}

// API serves the HTTP endpoints.
type API struct {
	store        Store
	log          *slog.Logger
	maxBodyBytes int64
	draining     atomic.Bool
}

// New returns an API backed by st.
func New(st Store, log *slog.Logger, maxBodyBytes int64) *API {
	return &API{store: st, log: log, maxBodyBytes: maxBodyBytes}
}

// StartDraining makes /readyz fail so Kubernetes stops sending traffic before
// the server shuts down.
func (a *API) StartDraining() { a.draining.Store(true) }

// Handler returns the full chain, outermost first: OpenTelemetry, access log,
// panic recovery, body limit, router. Middleware between otelhttp and the mux
// must pass the same *http.Request on: the mux sets r.Pattern on it, and
// otelhttp and the access log read it afterwards for the route.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	mux.HandleFunc("GET /v1/users/me", a.getMe)
	mux.HandleFunc("PUT /v1/users/me", a.putMe)

	h := routeWithErrors(mux)
	h = a.limitBody(h)
	h = a.recoverPanics(h)
	h = a.accessLog(h)
	return otelhttp.NewHandler(h, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r.URL.Path) }),
	)
}

func isProbe(path string) bool { return path == "/healthz" || path == "/readyz" }

func userID(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(userIDHeader))
}
