package telemetry

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// The handler is called directly: otel.SetErrorHandler is process-wide and
// cannot be undone (OTel's default handler forwards to the first one set).
func TestErrorHandler(t *testing.T) {
	// As in main: an exporting logger is the default, so log.Print reaches OTLP.
	exporting, _, exp := newExportingLogger(t, slog.LevelInfo, slog.LevelInfo)
	prev := slog.Default()
	slog.SetDefault(exporting)
	t.Cleanup(func() { slog.SetDefault(prev) })

	var stdout bytes.Buffer
	errorHandler(NewLogger(&stdout, slog.LevelWarn)).Handle(errors.New("exporter export timeout: connection refused"))

	lines := stdoutLines(t, &stdout)
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["msg"] != "opentelemetry error" ||
		!strings.Contains(lines[0]["error"].(string), "connection refused") {
		t.Errorf("stdout = %v, want one WARN opentelemetry error line", lines)
	}
	if got := exp.bodies(); len(got) != 0 {
		t.Errorf("exported %v, want nothing", got)
	}
}

// SetErrorLogger's logger has its own warn level, so LOG_LEVEL=error does not
// hide OTel errors; a logger at LOG_LEVEL would.
func TestErrorLoggerIgnoresLogLevel(t *testing.T) {
	var atLogLevel, own bytes.Buffer
	errorHandler(NewLogger(&atLogLevel, slog.LevelError)).Handle(errors.New("dropped"))
	if atLogLevel.Len() != 0 {
		t.Fatalf("a logger at LOG_LEVEL=error wrote %q; the test would prove nothing", atLogLevel.String())
	}
	errorHandler(NewLogger(&own, slog.LevelWarn)).Handle(errors.New("export failed"))
	if !strings.Contains(own.String(), "export failed") {
		t.Errorf("warn-level error logger wrote %q", own.String())
	}
}
