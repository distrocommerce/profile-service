package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// memExporter keeps exported records in memory.
type memExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memExporter) Export(_ context.Context, rs []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range rs {
		e.records = append(e.records, r.Clone())
	}
	return nil
}
func (e *memExporter) Shutdown(context.Context) error   { return nil }
func (e *memExporter) ForceFlush(context.Context) error { return nil }

// all returns a copy of the exported records.
func (e *memExporter) all() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdklog.Record(nil), e.records...)
}

func (e *memExporter) bodies() []string {
	var out []string
	for _, r := range e.all() {
		out = append(out, r.Body().AsString())
	}
	return out
}

// newExportingLogger returns a logger writing stdout JSON to the returned
// buffer and exporting synchronously to the returned exporter.
func newExportingLogger(t *testing.T, stdoutLevel, exportLevel slog.Level) (*slog.Logger, *bytes.Buffer, *memExporter) {
	t.Helper()
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	var buf bytes.Buffer
	return NewExportingLogger(&buf, stdoutLevel, lp, exportLevel), &buf, exp
}

func spanContext(t *testing.T) context.Context {
	t.Helper()
	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	return trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))
}

func attrs(r sdklog.Record) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool { m[string(kv.Key)] = kv.Value; return true })
	return m
}

// stdoutLines parses the JSON lines without the time field.
func stdoutLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("stdout line is not JSON: %q", l)
		}
		delete(m, "time")
		out = append(out, m)
	}
	return out
}

func TestExportLevel(t *testing.T) {
	logger, buf, exp := newExportingLogger(t, slog.LevelDebug, slog.LevelInfo)
	logger.Debug("probe")
	logger.Info("request")
	logger.Warn("slow")
	logger.With("component", "x").Debug("probe with attrs")

	if got := exp.bodies(); strings.Join(got, ",") != "request,slow" {
		t.Errorf("exported %v, want [request slow]", got)
	}
	if n := len(stdoutLines(t, buf)); n != 4 {
		t.Errorf("stdout has %d lines, want 4 (debug included)", n)
	}
}

func TestExportAtDebug(t *testing.T) {
	logger, _, exp := newExportingLogger(t, slog.LevelInfo, slog.LevelDebug)
	logger.Debug("probe")
	if got := exp.bodies(); len(got) != 1 || got[0] != "probe" {
		t.Errorf("exported %v, want [probe]", got)
	}
}

func TestStdoutQuieterThanExport(t *testing.T) {
	logger, buf, exp := newExportingLogger(t, slog.LevelError, slog.LevelInfo)
	logger.Info("request")
	if got := exp.bodies(); len(got) != 1 || got[0] != "request" {
		t.Errorf("exported %v, want [request]", got)
	}
	if buf.Len() != 0 {
		t.Errorf("stdout = %q, want nothing below error", buf.String())
	}
}

func TestExportedRecord(t *testing.T) {
	logger, buf, exp := newExportingLogger(t, slog.LevelInfo, slog.LevelInfo)
	ctx := spanContext(t)
	logger.InfoContext(ctx, "request", "method", "GET", "status", 200)
	logger.Info("migrations done")

	records := exp.all()
	if len(records) != 2 {
		t.Fatalf("exported %d records, want 2", len(records))
	}
	r := records[0]
	sc := trace.SpanContextFromContext(ctx)
	if r.Body().AsString() != "request" || r.Severity() != log.SeverityInfo {
		t.Errorf("body %q severity %v", r.Body().AsString(), r.Severity())
	}
	if r.TraceID() != sc.TraceID() || r.SpanID() != sc.SpanID() {
		t.Errorf("trace context %s/%s, want %s/%s", r.TraceID(), r.SpanID(), sc.TraceID(), sc.SpanID())
	}
	a := attrs(r)
	if a["method"].AsString() != "GET" || a["status"].AsInt64() != 200 {
		t.Errorf("attributes = %v", a)
	}
	if _, ok := a["trace_id"]; ok {
		t.Error("exported record has a trace_id attribute")
	}
	if _, ok := a["span_id"]; ok {
		t.Error("exported record has a span_id attribute")
	}
	if r.InstrumentationScope().Name != scopeName {
		t.Errorf("scope = %q", r.InstrumentationScope().Name)
	}

	if r := records[1]; r.TraceID().IsValid() || r.SpanID().IsValid() {
		t.Errorf("record without a span has trace context %s/%s", r.TraceID(), r.SpanID())
	}

	lines := stdoutLines(t, buf)
	if len(lines) != 2 {
		t.Fatalf("stdout has %d lines, want 2", len(lines))
	}
	if lines[0]["trace_id"] != sc.TraceID().String() || lines[0]["span_id"] != sc.SpanID().String() {
		t.Errorf("stdout line = %v, want trace_id and span_id", lines[0])
	}
}

func TestStdoutUnchangedByExport(t *testing.T) {
	logAll := func(l *slog.Logger) {
		ctx := spanContext(t)
		l.InfoContext(ctx, "request", "method", "GET", "status", 200)
		l.With("component", "store").Warn("slow query", "ms", 12.5)
		l.WithGroup("g").Error("failed", "error", "boom")
	}
	var plain bytes.Buffer
	logAll(NewLogger(&plain, slog.LevelInfo))
	logger, exported, _ := newExportingLogger(t, slog.LevelInfo, slog.LevelInfo)
	logAll(logger)

	a, b := stdoutLines(t, &plain), stdoutLines(t, exported)
	if len(a) != 3 {
		t.Fatalf("stdout-only logger wrote %d lines, want 3", len(a))
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("stdout differs with export:\nwithout %s\nwith    %s", ja, jb)
	}
}

func TestNewExportingLoggerNilProvider(t *testing.T) {
	var buf bytes.Buffer
	logger := NewExportingLogger(&buf, slog.LevelInfo, nil, slog.LevelInfo)
	logger.Info("request")
	if lines := stdoutLines(t, &buf); len(lines) != 1 || lines[0]["msg"] != "request" {
		t.Errorf("stdout = %v, want one request line", lines)
	}
	if _, ok := logger.Handler().(traceHandler); !ok {
		t.Errorf("handler = %T, want the stdout-only traceHandler", logger.Handler())
	}
}
