# logging Specification

## Purpose

The service's log output: structured JSON on stdout for `kubectl logs`, and the same records exported over
OTLP to the collector so they can be searched in Loki and reached from a trace.

## Requirements

### Requirement: JSON logs on stdout are unchanged
The service SHALL keep writing one JSON object per record to stdout at the level set by `LOG_LEVEL`, with the
same fields as before, including `trace_id` and `span_id` when the record is logged within a span. This holds
whether or not logs are also exported.

#### Scenario: Request within a span
- **WHEN** a request to `/v1/users/me` is served and its access line is logged
- **THEN** stdout has a JSON line with `msg` `request`, `method`, `path`, `route`, `status`, `duration_ms`, `trace_id` and `span_id`

#### Scenario: Export enabled
- **WHEN** OTLP export is enabled
- **THEN** every record that reaches stdout is still written there in the same format

### Requirement: Logs are exported over OTLP
When the OpenTelemetry SDK is enabled, the service SHALL export its log records over OTLP/gRPC to the endpoint
in the standard `OTEL_EXPORTER_OTLP_*` variables, with the same resource as traces and metrics
(`service.name`, `service.version`, `service.instance.id` and the other resource attributes). Each record SHALL
keep its message, severity and attributes.

#### Scenario: Access line exported
- **WHEN** a request is served with export enabled
- **THEN** the collector receives a log record with body `request`, severity INFO, the attributes `method`, `path`, `route`, `status` and `duration_ms`, and resource attribute `service.name` `profile`

### Requirement: Exported records carry the trace context
A record logged within a span SHALL carry that span's trace ID and span ID in the OTLP record's trace context
fields, not as `trace_id`/`span_id` attributes, so the backend sees each ID once. A record logged without a span
SHALL have no trace context.

#### Scenario: Record within a request
- **WHEN** the access line of a traced request is exported
- **THEN** the OTLP record's trace ID and span ID equal those of the request's server span, and it has no `trace_id` or `span_id` attribute

#### Scenario: Record outside a request
- **WHEN** a startup line such as `migrations done` is exported
- **THEN** the OTLP record has no trace context

### Requirement: The exported level is set separately
The minimum level of exported records SHALL come from `LOG_EXPORT_LEVEL` (`debug`, `info`, `warn` or `error`,
default `info`), independent of `LOG_LEVEL`, which applies to stdout only. An invalid value SHALL stop the
service at startup with the other configuration errors.

#### Scenario: Probe lines stay off OTLP by default
- **GIVEN** `LOG_LEVEL=debug` and `LOG_EXPORT_LEVEL` unset
- **WHEN** the kubelet calls `/healthz` and `/readyz`
- **THEN** their debug access lines appear on stdout and are not exported

#### Scenario: Export at debug level
- **WHEN** `LOG_EXPORT_LEVEL=debug`
- **THEN** debug records are exported too

#### Scenario: Stdout quieter than export
- **GIVEN** `LOG_LEVEL=error` and `LOG_EXPORT_LEVEL=info`
- **WHEN** an info record is logged
- **THEN** it is exported and not written to stdout

#### Scenario: Invalid value
- **WHEN** `LOG_EXPORT_LEVEL=verbose`
- **THEN** the service exits at startup and the error names `LOG_EXPORT_LEVEL`

### Requirement: Disabling the SDK keeps stdout-only logging
With `OTEL_SDK_DISABLED=true` the service SHALL export no logs and attempt no connection for them, and SHALL
log to stdout exactly as with export enabled.

#### Scenario: SDK disabled
- **WHEN** the service runs with `OTEL_SDK_DISABLED=true`
- **THEN** records appear on stdout and nothing is sent over OTLP

### Requirement: Shutdown flushes buffered logs
On shutdown the service SHALL flush buffered log records together with traces and metrics, after the HTTP
server has stopped, within the existing telemetry shutdown timeout; a slow trace or metric flush MUST NOT use up
the time of the log flush. The error that ends the process, if any, SHALL be logged before that flush. Records
logged after the flush SHALL still reach stdout.

#### Scenario: Shutdown lines reach the backend
- **WHEN** the pod receives SIGTERM and shuts down gracefully
- **THEN** the `shutting down` and `stopped` records are exported before the process exits

#### Scenario: Slow trace flush
- **WHEN** the trace flush takes the whole shutdown timeout
- **THEN** the log flush still runs with its own share of that time, not after it

#### Scenario: Fatal error reaches the backend
- **WHEN** the service stops with an error after telemetry is set up (for example the server cannot listen)
- **THEN** the `exit` record with that error is exported, and it is on stdout too

### Requirement: Export problems do not affect the service
If the collector is unreachable or rejects records, the service SHALL keep serving requests and keep logging to
stdout. OpenTelemetry's own errors, such as failed exports, SHALL be logged to stdout at warn level, whatever
`LOG_LEVEL` is, and MUST NOT be exported over OTLP, so a failing export cannot feed records back into the exporter.

#### Scenario: Collector down
- **WHEN** the collector is unreachable while requests are served
- **THEN** responses are unchanged and every record still reaches stdout

#### Scenario: Export error stays on stdout
- **WHEN** the OpenTelemetry SDK reports an error, for example a timed-out export
- **THEN** stdout has a WARN line with the error, and no record for it is exported

#### Scenario: Export error with a quiet stdout
- **GIVEN** `LOG_LEVEL=error`
- **WHEN** the OpenTelemetry SDK reports an error
- **THEN** stdout still has the WARN line with the error
