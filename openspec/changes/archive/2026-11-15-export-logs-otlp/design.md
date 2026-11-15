# Design

## Context

See proposal.md for the motivation; requirements are in `specs/logging`.

Current state:
- `telemetry.NewLogger(w, level)` wraps `slog.NewJSONHandler` in `traceHandler`, which adds `trace_id`/`span_id`
  attributes from the context. `main` installs it twice: at `info` before the configuration is read, then at
  `LOG_LEVEL`. `httpapi` logs with the `*Context` methods, so request records carry the span context.
- `telemetry.Setup` builds one resource (default `service.name` `profile`, `service.version`, then
  `OTEL_*` from the environment) and the trace and metric providers with OTLP/gRPC exporters; with
  `OTEL_SDK_DISABLED=true` it returns a no-op shutdown. `main` runs the returned shutdown in a `defer` with a 5 s
  timeout, after the server has stopped.
- Access lines for `/healthz` and `/readyz` are logged at debug (`httpapi.accessLog`).
- Infra: the collector receives OTLP on 4317 and has a `logs` pipeline (`memory_limiter`, `batch`) to Loki's
  `/otlp` endpoint; Loki 3.7.9 runs with defaults (schema v13, tsdb, no `otlp_config` overrides).
- Checked on Go 1.27.3: `log/slog.NewMultiHandler` exists; its `Handle` calls each handler that is enabled for
  the record's level. `sdk/log` takes the trace and span ID from the context passed to `Emit`
  (`logger.go:131` in v1.47.0). `otelslog.Handler.Enabled` asks the SDK logger, which has no level limit.
- OTel's errors (failed exports and the like) go to its global error handler, whose default calls `log.Print`
  (`internal/errorhandler/errorhandler.go:41` in v1.47.0). `slog.SetDefault` redirects the standard `log`
  package into the default slog logger at INFO, so without a handler of our own these errors would reach the
  OTLP branch. (Only OTel's internal debug logging uses its own `log.New(os.Stderr, …)` logger.) Found by
  verification: with the collector down, `exporter export timeout` lines appeared on stdout as INFO records.

## Goals / Non-Goals

**Goals:**
- One logger, two outputs, with each output's level set independently.
- No change for callers: `httpapi` and `main` keep using a `*slog.Logger`.
- Trace context in exported records exactly once.

**Non-Goals:**
- No global `LoggerProvider` (`otel/log/global`): nothing else in the process logs through the OTel log API.
- No OTLP/HTTP, no custom batch settings (the standard `OTEL_BLRP_*` variables still apply).
- No change to the stdout format or to `LOG_LEVEL`.

## Decisions

**Fan-out with `slog.NewMultiHandler`.** The logger's handler is
`slog.NewMultiHandler(traceHandler{JSON at LOG_LEVEL}, minLevel{otelslog handler, LOG_EXPORT_LEVEL})`.
`traceHandler` stays on the stdout branch only: on the OTLP branch the SDK already puts the IDs into the record's
trace context, and an extra `trace_id` attribute would reach Loki twice. Alternatives: a hand-written fan-out
handler (the standard library has one since Go 1.26); the `otelslog` handler alone (loses stdout, which
`kubectl logs` and the collector-down case need).

**Export level: a small `minLevel` wrapper, default `info`.** `minLevel.Enabled` returns false below the level,
so the bridge never builds a record for it. Alternative: a severity-filtering `sdk/log` processor; it filters
only after the bridge has converted the record and needs more code. The setting is a new variable
`LOG_EXPORT_LEVEL` (parsed like `LOG_LEVEL`), not `OTEL_LOG_LEVEL`: in the OpenTelemetry environment
specification that name is the SDK's own internal log level, so reusing it would be misleading. Default `info`
keeps probe access lines (debug, every few seconds per pod) out of Loki while stdout can still show them.

**Logger API.** `NewLogger(w io.Writer, level slog.Level)` stays the stdout-only logger, so existing callers and
tests do not change. A second constructor `NewExportingLogger(w, level, provider *sdklog.LoggerProvider,
exportLevel)` adds the OTLP branch; with a nil provider it returns the stdout-only logger. Changed after code
review: a functional option (`WithOTLP`) carried only two values and took a `log.LoggerProvider` interface, where
a typed-nil `*sdklog.LoggerProvider` is non-nil; the concrete pointer type makes nil a plain check. The bridge
handler is `otelslog.NewHandler("github.com/distrocommerce/profile-service", otelslog.WithLoggerProvider(p))`;
the name is the instrumentation scope. No scope version: the resource already carries `service.version`.

**Setup returns the log provider.** `Setup` additionally builds an `sdk/log` `LoggerProvider` with the same
resource, an `otlploggrpc` exporter (configured by `OTEL_EXPORTER_OTLP_ENDPOINT` or
`OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`) and a `BatchProcessor`, and returns it alongside the shutdown function (a
small struct). With `OTEL_SDK_DISABLED=true` the provider is nil, so `NewExportingLogger` builds no OTLP branch and
no exporter or connection exists. The shutdown runs the trace, metric and log providers' `Shutdown` in parallel
on the shared 5 s context and joins their errors; it runs once, after `srv.Shutdown`. Changed after code review:
the first version ran them one after another with logs last, so a trace flush that hangs until the deadline left
the log flush an expired context and the last records (`shutting down`, `stopped`, the drain's requests) were
lost. Alternative: a separate timeout per provider; parallel is simpler and gives each the full 5 s.

**Wiring in `main`.** Order: stdout logger at `info` → config → stdout logger at `LOG_LEVEL` (`stdoutLog`) →
error logger → `Setup` → deferred telemetry shutdown (logs through `stdoutLog`) → `NewExportingLogger` (`log`) →
`slog.SetDefault` → deferred `exit` logging → store, server. `run` has a named error result; the `exit` defer is
registered after the shutdown defer, so it runs first and the error that ends the process is logged through the
exporting logger before the flush (changed after code review: before, `main` logged it after the flush, so the
reason for a crash loop never reached Loki). `run` then wraps the error so `main` does not log it twice; errors
before `Setup` (configuration, exporter creation) are still logged by `main`, to stdout only. The shutdown
warning uses `stdoutLog` explicitly, not the reassigned `log` variable, so it does not depend on what the SDK
does with records after shutdown.

**Error handler: stdout only, at warn.** `main` calls `telemetry.SetErrorLogger(os.Stdout)` right after
reading the configuration: it calls `otel.SetErrorHandler` with a handler that logs `opentelemetry error` at WARN
through a stdout-only logger with its own fixed level `warn` (changed after code review: with `LOG_LEVEL`, a
setting of `error` dropped every export failure silently). OTel errors stay visible in
`kubectl logs` and never reach the log exporter, so a failing export cannot feed itself. Alternatives: leaving
the default (`log.Print`, which `slog.SetDefault` routes into the exporting logger: the loop verification found);
not calling `slog.SetDefault` (the `log` redirect is part of it, and other `log.Print` users are rare but should
still be logged); filtering the OTLP branch by message (fragile).

**Versions.** The set that requires exactly `otel` v1.47.0 and `otel/log` v1.47.0 (checked in each module's
`go.mod`): `sdk/log` v1.47.0, `otlploggrpc` v0.23.0, `otelslog` v0.21.0. The
previous releases (`otlploggrpc` v0.22.0, `otelslog` v0.20.1) require v1.46.0 / `log` v0.22.0.

**Tests.** The `telemetry` tests use the real `sdk/log` `LoggerProvider` with a `SimpleProcessor` and an
in-memory exporter, so they check the records exactly as they would be exported (trace ID, span ID, severity,
attributes, scope). `logtest.Recorder` was not used: it stores only the context, not the trace context the SDK
writes into the record. Records at and above the export level are exported and lower ones are not; a record logged within a span has the span's trace and
span ID as trace context and no `trace_id` attribute; the stdout JSON is identical with and without
the OTLP branch (apart from the timestamp). `config` tests cover `LOG_EXPORT_LEVEL` (default, valid values, invalid value in the error list).
`Setup` with `OTEL_SDK_DISABLED=true` returns a nil provider. The OTel error handler, called directly (no global
state), with an exporting logger as the slog default, writes a WARN line to stdout and exports nothing.
`shutdownAll` is tested with a provider that blocks until the deadline: the others still finish at once. Export over the network is checked in the e2e
step, not in unit tests.

## Risks / Trade-offs

- [`otlploggrpc` and `otelslog` are 0.x and may change their API] → Pinned; upgraded together with the otel set,
  using the same `go.mod` check as above.
- [Loki's default mapping is unverified: which resource attributes become index labels, and where `trace_id`
  lands] → The e2e task queries Loki and records the names (expected: label `service_name`, structured metadata
  `trace_id`/`span_id`); the infra repo's trace-to-logs link uses them.
- [Request attributes (`path`, `status`, …) becoming Loki labels would explode cardinality] → With Loki's
  defaults, log attributes go to structured metadata, not labels; the e2e task confirms it.
- [Collector down: the batch processor's queue (default 2048 records) fills and drops] → Stdout keeps every
  record; requests are unaffected (the export is asynchronous).
- [If infra later ships container stdout to Loki, every line arrives twice] → Reported to the infra repo as a
  constraint for its open question (skip services that export over OTLP).
- [Configuration and exporter-creation errors are not exported (there is no exporter yet)] → They are on
  stdout; a service that cannot start is visible through the pod status anyway. Errors after `Setup` are exported.

## Migration Plan

No data or API change. Deploy with `make image kind-load deploy PROJECT_ID=<id>`; logs appear in Loki for
`service_name=profile`. Rollback: redeploy the previous image; stdout logging is the same in both versions.
