# Tasks

## 1. Configuration

- [x] 1.1 Add `LogExportLevel` to `internal/config` from `LOG_EXPORT_LEVEL` (default `info`, parsed like `LOG_LEVEL`, invalid value added to the startup error list naming `LOG_EXPORT_LEVEL`); verify with `config_test.go` cases for the default, each valid value and an invalid value, and `go test ./internal/config/`.
- [x] 1.2 Document `LOG_EXPORT_LEVEL` in the README configuration table; verify the default and the values match the config tests.

## 2. Log provider and fan-out logger

- [x] 2.1 Add the pinned modules (`go.opentelemetry.io/otel/sdk/log` v1.47.0, `go.opentelemetry.io/otel/log` v1.47.0, `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc` v0.23.0, `go.opentelemetry.io/contrib/bridges/otelslog` v0.21.0) and run `go mod tidy`; verify `go.mod` lists exactly these versions, every other `go.opentelemetry.io/otel*` module stays at v1.47.0, and `go build ./...` passes.
- [x] 2.2 In `telemetry.Setup`, build an `sdk/log` `LoggerProvider` with the shared resource, an `otlploggrpc` exporter and a `BatchProcessor`; return it with the shutdown function, which now joins trace, metric and log provider shutdown (log last); with `OTEL_SDK_DISABLED=true` return a nil provider. Verify with a test that the disabled path returns a nil provider and a working no-op shutdown, and `go build ./...`.
- [x] 2.3 Extend `telemetry.NewLogger` with `LoggerOption` / `WithOTLP(provider, level)`: `slog.NewMultiHandler` of the stdout `traceHandler` (JSON at `LOG_LEVEL`) and a `minLevel`-wrapped `otelslog` handler (scope `github.com/distrocommerce/profile-service`). Verify with tests against the real `sdk/log` provider and an in-memory exporter: records at and above the export level are recorded, lower ones are not; a record logged within a span carries the span's trace and span ID as trace context and has no `trace_id`/`span_id` attribute; a record without a span has no trace context; message, severity and attributes are kept; the stdout JSON is identical with and without `WithOTLP` (apart from the timestamp); `LOG_LEVEL=error` with export at `info` exports an info record without writing it to stdout.

## 3. Wiring and docs

- [x] 3.1 In `cmd/profile-service`, rebuild the logger after `telemetry.Setup` with `WithOTLP(provider, cfg.LogExportLevel)` when the provider is non-nil, set it as the default, and keep the single deferred telemetry shutdown after `srv.Shutdown`; verify `go build ./...`, `make test`, and `make run` (which sets `OTEL_SDK_DISABLED=true`) logging to stdout as before with no OTLP connection attempts in its output.
- [x] 3.2 Update the README (logging and telemetry sections: logs over OTLP, levels, what stays stdout-only, `OTEL_SDK_DISABLED`) and the `CLAUDE.md` fixed decision to "OpenTelemetry for traces, metrics and logs"; verify the README matches the spec scenarios.

## 4. Integration checks

- [x] 4.1 Run `make test lint`; verify all pass.
- [x] 4.2 Deploy with `make image kind-load deploy PROJECT_ID=<zitadel project id>`, send requests through Envoy with a token (GET and PUT on `/v1/users/me`, noting existing rows first and deleting only test rows afterwards) and query Loki (`kubectl port-forward` to the Loki service, `/loki/api/v1/...`); verify: records exist for `service.name` `profile` with the access-line attributes; no debug probe lines are exported; an exported access line's trace ID matches the request's trace in Tempo; request attributes are structured metadata, not labels. Ask the user to confirm the same in Grafana Explore → Loki.
- [x] 4.3 Record the Loki names for `service.name` (index label) and for the trace and span ID (structured metadata or other), as reported by the Loki API, in the final report for the infra repo's trace-to-logs link, together with the stdout-shipping duplication note.
- [x] 4.4 Restart the pod (`kubectl rollout restart deployment/profile -n apps`); verify the old pod's `shutting down` and `stopped` records are in Loki.

## 5. Verification fixes

- [x] 5.1 Add `telemetry.SetErrorLogger(l *slog.Logger)` (installs `otel.SetErrorHandler` logging `opentelemetry error` at WARN through `l`) and call it in `cmd/profile-service` right after loading the configuration, with a stdout-only logger at `LOG_LEVEL`; verify with a `telemetry` test in which an exporting logger is the slog default and `otel.Handle(err)` writes a WARN line with the error to stdout and exports nothing, and with `make test`.
- [x] 5.2 Mention in the README logging section that OpenTelemetry's own errors are logged to stdout at warn and never exported; verify it matches the spec scenario "Export error stays on stdout".
- [x] 5.3 Run `make test lint`, then rerun the local collector-down check (binary with `OTEL_EXPORTER_OTLP_ENDPOINT` pointing at a closed port, stdout and stderr captured separately); verify requests are answered, every record is on stdout, and OTel errors appear only as WARN `opentelemetry error` lines.

## 6. Code review fixes

- [x] 6.1 Shut the trace, metric and log providers down in parallel (a `shutdownAll` helper in `internal/telemetry`, also used on the `runtime.Start` error path); verify with a test in which one shutdown blocks until the context deadline and the others complete immediately and are all called, and `go test -race ./internal/telemetry/`.
- [x] 6.2 Replace `LoggerOption`/`WithOTLP` with `NewExportingLogger(w, level, provider *sdklog.LoggerProvider, exportLevel)` (nil provider: stdout-only logger); update the tests to use it, add a nil-provider test, check `lines` lengths before indexing, and read exported records only through a locked accessor; verify with `go test -race ./internal/telemetry/`.
- [x] 6.3 Make the OTel error logger a stdout-only logger at a fixed `warn` level, and test the handler by calling it directly (no global `otel.SetErrorHandler` in tests), including with a stdout logger whose own level would be `error`; verify with `go test ./internal/telemetry/`.
- [x] 6.4 In `cmd/profile-service`, give `run` a named error result and register an `exit` defer after the telemetry-shutdown defer that logs the error through the exporting logger and wraps it so `main` does not log it again; log the shutdown warning through the stdout-only logger variable. Verify with `go build ./...`, `make test`, and a local run that fails after `Setup` (port already in use) printing the `exit` line once.
- [x] 6.5 README: fix the configuration table alignment, state that the `exit` error is exported when telemetry is set up, and that OTel errors are logged at warn whatever `LOG_LEVEL` is; verify against the spec scenarios.
- [x] 6.6 Run `make test lint`; redeploy and verify in Loki that records still arrive; verify "Fatal error reaches the backend" by running the binary locally with `OTEL_EXPORTER_OTLP_ENDPOINT` set to a port-forward of the collector and its port already in use, then finding its `exit` record in Loki.

## Workflow follow-up

- Run `/openspec-verify-change` and the code review before archiving.
- Archive with `/openspec-archive-change`; commit the logs change separately from the pending `.agents/` and `CLAUDE.md` workflow edits, after approval.
