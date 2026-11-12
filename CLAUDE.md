# profile-service

First DistroCommerce microservice and the template for the others: keep it small, idiomatic and
production-shaped. Infrastructure (kind cluster, Envoy Gateway, ZITADEL, observability, the
`service-lib` Helm library chart) lives in the sibling checkout `../distrocommerce-infra`.

## Fixed decisions (do not reopen unless asked)
- Go 1.27.3, standard library `net/http` ServeMux (no web framework), pgx v5, `log/slog` JSON logs.
- Endpoints: `GET /healthz` (liveness, no dependencies), `GET /readyz` (readiness, checks the DB),
  public API under `/v1/`.
- Every error is RFC 9457 `application/problem+json` with a stable `code` field, including router 404/405,
  413 and recovered panics.
- Request bodies are limited (`MAX_BODY_BYTES`, default 1 MiB) → 413.
- OpenTelemetry for traces and metrics (OTLP gRPC to the collector); probes are not traced.
- SQL migrations are embedded and applied at startup.
- Multi-stage Docker build, distroless `static-debian13:nonroot` runtime, non-root.
- Images must have linux/arm64 builds (Apple silicon). Pin every version; never `latest`.
- Envoy validates the JWT and sets `X-User-ID` to the token `sub`. The service trusts that header and does
  no JWT validation itself.

## Layout
- `cmd/profile-service/`: main (wiring, server, graceful shutdown only)
- `internal/config`: environment configuration, fail-fast validation
- `internal/httpapi`: routes, handlers, middleware; `errors.go` holds the error codes and `writeError`
- `internal/store`: pgx store; migrations in `internal/store/migrations/NNNN_name.sql`
- `internal/telemetry`: OTel providers, slog logger with `trace_id`
- `deploy/chart/`: thin Helm chart over `service-lib`, plus `values-local.yaml`
- `Makefile`: build, test, lint, run, image, kind-load, deploy, undeploy

## How to work here
- Before finishing a change: `make test lint` (go vet, tests, golangci-lint) must pass. `make lint` uses
  `go run` with a pinned golangci-lint, because a prebuilt binary built with an older Go refuses this module.
- New endpoints: handler test with `httptest` for success and every error case; errors use the shared helper.
  Request bodies go through `decodeJSON`, so the body limit and the 413 come from one place.
- Middleware between `otelhttp` and the mux must pass the same `*http.Request` on (no `r.WithContext`).
  The mux sets `r.Pattern` on it, and the span name, `http.route` and the access log read it afterwards.
- Migrations are append-only: add a new numbered file, never edit an applied one.
- Check APIs and versions (`go doc`, module docs, `helm template`) instead of guessing; say when something is unverified.
- Deploy loop: `make image kind-load deploy PROJECT_ID=<zitadel project id>`. Do not reset or recreate the kind
  cluster or the compose stores from this repo. The project ID changes after an infra reset, so it is never
  committed. `make run` listens on 8081 because kind holds 8080.
- Never copy library templates; propose changes to the infra repo instead. If `service-lib` cannot express
  something this service needs, stop and describe the change for `../distrocommerce-infra`; do not work
  around it in `deploy/chart`, and do not edit the infra repo from here.
- Never commit secrets. All code, comments, docs and commit messages are in English. No AI attribution in commits.
- Never commit without the user's approval: propose the files and the message, then wait. One approval
  covers one commit. Keep commits to logical steps on `main`; push only when asked.
