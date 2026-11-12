# profile-service

DistroCommerce profile service: stores and serves user profile data, keyed by the ZITADEL user ID
(token `sub`) that Envoy Gateway passes in `X-User-ID`. Go, PostgreSQL, OpenTelemetry; deployed
with the shared Helm library chart. It is the template for the other services.

## API

| Endpoint            | Description                                                                 |
|---------------------|-----------------------------------------------------------------------------|
| `GET /healthz`      | Liveness. No dependencies; always 200 while the process serves.             |
| `GET /readyz`       | Readiness. Pings PostgreSQL; 503 if it fails or the server is shutting down. |
| `GET /v1/users/me`  | The caller's profile. 401 without `X-User-ID`, 404 if no profile row exists. |

```json
{"user_id":"312345678901234567","display_name":"Ada","email":"ada@example.com",
 "created_at":"2026-11-01T12:00:00Z","updated_at":"2026-11-01T12:00:00Z"}
```

**Trust model.** Envoy validates the JWT and sets `X-User-ID` from `sub`, overwriting any client
value. The service trusts the header and does no JWT validation, so it must only be reachable
through Envoy.

**Errors** are always [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) `application/problem+json`,
including router 404/405, 413 and panics. `type` is `about:blank`, `title` the status phrase, and
`code` a stable machine-readable identifier:

```json
{"type":"about:blank","title":"Not Found","status":404,"detail":"no profile exists for this user",
 "instance":"/v1/users/me","code":"profile_not_found"}
```

Codes: `unauthenticated` (401), `profile_not_found` (404), `not_found` (404, unknown path),
`method_not_allowed` (405), `invalid_body` (400), `request_too_large` (413), `internal_error`
(500), `database_unavailable` / `shutting_down` (503, readiness).

## Configuration

Environment variables only; invalid values stop the service at startup with all errors listed.

| Variable           | Default  | Description                                       |
|--------------------|----------|---------------------------------------------------|
| `DATABASE_URL`     | required | PostgreSQL URL (pgx format)                       |
| `PORT`             | `8080`   | HTTP port                                         |
| `LOG_LEVEL`        | `info`   | `debug`, `info`, `warn`, `error`                  |
| `SHUTDOWN_TIMEOUT` | `20s`    | Total time for graceful shutdown (Go duration)    |
| `MAX_BODY_BYTES`   | `1048576`| Request body limit (413 above it)                 |

OpenTelemetry uses the standard `OTEL_*` variables (`OTEL_SERVICE_NAME`,
`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_TRACES_SAMPLER`, `OTEL_RESOURCE_ATTRIBUTES`, ...). Traces and
metrics go over OTLP/gRPC; an `http://` endpoint means plaintext. `OTEL_SDK_DISABLED=true`
turns telemetry off. The Helm chart sets the service name (`profile`) and the collector endpoint.

## Behavior

- **Migrations**: SQL files in `internal/store/migrations` are embedded in the binary and applied
  at startup in name order, each in a transaction, recorded in `schema_migrations`. A PostgreSQL
  advisory lock serializes replicas that start together.
- **Shutdown** on SIGTERM/SIGINT: `/readyz` starts failing, the server keeps serving for 5 s
  (so Envoy and the endpoints stop routing here), then drains in-flight requests within
  `SHUTDOWN_TIMEOUT`, then flushes telemetry. A second signal kills the process at once.
- **Logging**: JSON on stdout (`log/slog`). One line per request with `method`, `path`, `route`,
  `status`, `duration_ms` and `trace_id`; probe requests are logged at debug level.
- **Tracing**: server spans named after the route (`GET /v1/users/me`), continuing the trace
  started at Envoy; one span per SQL query; `/healthz` and `/readyz` are not traced.

## Layout

```
cmd/profile-service   main: wiring, server, graceful shutdown
internal/config       environment configuration
internal/httpapi      routes, handlers, middleware, error responses
internal/store        PostgreSQL (pgx) and migrations
internal/telemetry    OpenTelemetry providers, slog logger
deploy/chart          Helm chart (depends on service-lib)
```

## Local development

Prerequisites: Go 1.27.3, Docker, and the local environment from
[distrocommerce-infra](../distrocommerce-infra) (`docs/local-environment.md`), checked out next to
this repository: the compose PostgreSQL, the kind cluster `distro`, Envoy Gateway, ZITADEL and the
observability stack.

```sh
make test   # go vet + go test -race
make lint   # golangci-lint (pinned, built with the local Go)
make run    # http://localhost:8081, against the compose PostgreSQL on localhost:5432
make build  # bin/profile-service
```

`make run` uses port 8081 because kind holds 8080. The store integration test needs a database
(it works in a throwaway schema):

```sh
PROFILE_TEST_DATABASE_URL='postgres://profile:profile_dev@localhost:5432/profile?sslmode=disable' go test ./internal/store/
```

## Deploy to the local cluster

The `profile-db` secret (`DATABASE_URL`) comes from `make db-secrets` in the infra repository. The
image tag is the short git SHA of `HEAD`, so commit first.

```sh
make image                              # profile:<sha>, host platform (arm64)
make kind-load                          # into the kind cluster "distro"
make deploy PROJECT_ID=<zitadel project id>
kubectl get pods,httproute,securitypolicy -n apps
make undeploy
```

The ZITADEL project ID is the token audience. It changes after an infra reset, so it is passed at
deploy time (`--set-string`, because the library chart rejects unquoted numbers) instead of being
committed.

Check through Envoy (get `$TOKEN` as in step 5 of the runbook):

```sh
curl -i http://api.localtest.me:8080/v1/users/me                                   # 401 from Envoy
curl -i -H "Authorization: Bearer $TOKEN" http://api.localtest.me:8080/v1/users/me # 404 problem+json until a profile row exists
```

Traces: Grafana (http://grafana.localtest.me:8080) → Explore → Tempo, `service.name=profile`.
