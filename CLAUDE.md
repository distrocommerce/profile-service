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
- `make test` skips the store test. After changing SQL or migrations, also run it against the compose
  PostgreSQL with `PROFILE_TEST_DATABASE_URL` (see the README); it works in a throwaway schema.
- `/readyz` must answer within the kubelet probe timeout (1 s; `service-lib` sets none): every dependency
  check gets a timeout below that (the DB ping uses 800 ms).
- The pod runs with the `service-lib` security defaults: non-root, read-only root filesystem, no capabilities.
  Never write files at runtime (no temp files, no local caches); if that becomes necessary, propose a writable
  volume in `service-lib` instead of turning the defaults off here.
- Chart values: shared defaults in `deploy/chart/values.yaml`, only environment-specific settings in
  `values-local.yaml` (and later one file per environment).
- Check APIs and versions (`go doc`, module docs, `helm template`) instead of guessing; say when something is unverified.
- Deploy loop: `make image kind-load deploy PROJECT_ID=<zitadel project id>`. Do not reset or recreate the kind
  cluster or the compose stores from this repo. The project ID changes after an infra reset, so it is never
  committed. `make run` listens on 8081 because kind holds 8080.
- The image tag is the short SHA, plus `-dirty-<hash of the changes>` for an uncommitted tree: a changed tree
  must get a new tag or `helm upgrade` is a no-op and the old pod keeps running. Keep that suffix; do not
  commit just to deploy.
- Never copy library templates; propose changes to the infra repo instead. If `service-lib` cannot express
  something this service needs, stop and describe the change for `../distrocommerce-infra`; do not work
  around it in `deploy/chart`, and do not edit the infra repo from here.
- Never commit secrets. All code, comments, docs and commit messages are in English. No AI attribution in commits.
- Never commit without the user's approval: propose the files and the message, then wait. One approval
  covers one commit. Keep commits to logical steps on `main`; push only when asked.
