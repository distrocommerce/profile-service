# Proposal

## Why

The service's logs exist only on pod stdout, and nothing ships stdout to Loki: infra decided that logs reach
Loki over OTLP from the services for now (`distrocommerce-infra` `docs/architecture.md`), and the collector's
`logs` pipeline to Loki is already running. So today the logs cannot be searched in Grafana or reached from a
trace, while traces and metrics already go over OTLP.

## What Changes

- Export logs through the OpenTelemetry log SDK over OTLP/gRPC to the collector, in addition to the JSON logs
  on stdout. One `slog` logger fans out to both: the stdout JSON handler (unchanged) and the `otelslog`
  bridge.
- Logs written within a request carry the trace and span ID in the OTLP record's trace context, so Loki can
  link them to Tempo. Stdout keeps its `trace_id`/`span_id` fields.
- New setting `LOG_EXPORT_LEVEL` (default `info`): the minimum level exported over OTLP, independent of
  `LOG_LEVEL` (stdout). Debug records, such as the probe access lines, stay off OTLP by default.
- `OTEL_SDK_DISABLED=true` keeps stdout-only logging, as it already disables traces and metrics.
- Shutdown flushes buffered logs together with traces and metrics.
- The fixed decision "OpenTelemetry for traces and metrics" in `CLAUDE.md` becomes "traces, metrics and logs".
- After deployment, report the Loki label and structured-metadata names that `service.name` and `trace_id`
  get, for the trace-to-logs link in the infra repo.

Out of scope: shipping container stdout (Alloy or filelog, an infra decision), Grafana's trace-to-logs
configuration (infra repo), log sampling, OTLP over HTTP, changes to the log format on stdout.

## Capabilities

### New Capabilities
- `logging`: the service's log output: JSON on stdout, OTLP export with trace correlation, levels, and
  behavior on shutdown and with the SDK disabled.

### Modified Capabilities
None (`user-profile` and `api-errors` do not change).

## Impact

- Code: `internal/telemetry` (log provider in `Setup`, fan-out logger), `internal/config` (`LOG_EXPORT_LEVEL`),
  `cmd/profile-service` (build the logger after telemetry setup, flush on shutdown).
- Dependencies (direct, pinned to the otel v1.47 set): `go.opentelemetry.io/otel/sdk/log` v1.47.0,
  `go.opentelemetry.io/otel/log` v1.47.0 (already indirect), `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc`
  v0.23.0, `go.opentelemetry.io/contrib/bridges/otelslog` v0.21.0.
- Deployment: none. `service-lib` already sets `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_SERVICE_NAME`, which the
  log exporter reads too; the collector's `logs` pipeline exists.
- Infra (to report, not to change from here): if the infra repo later ships container stdout to Loki, it must
  skip services that export over OTLP, or every line arrives twice.
- Docs: README configuration table and logging section; `CLAUDE.md` fixed decisions.
