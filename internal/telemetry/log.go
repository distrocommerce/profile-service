package telemetry

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// scopeName is the instrumentation scope of exported log records.
const scopeName = "github.com/distrocommerce/profile-service"

// NewLogger returns a JSON logger for w at level that adds trace_id and span_id
// to records logged with a context that carries a span (the *Context logging
// methods).
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(stdoutHandler(w, level))
}

// NewExportingLogger is NewLogger plus export: records at exportLevel and
// above also go to provider (the log provider from Setup) through the otelslog
// bridge, each output with its own level. The SDK takes the trace and span ID
// from the context into the record's trace context, so exported records carry
// no trace_id/span_id attributes. A nil provider gives the stdout-only logger.
func NewExportingLogger(w io.Writer, level slog.Level, provider *sdklog.LoggerProvider, exportLevel slog.Level) *slog.Logger {
	if provider == nil {
		return NewLogger(w, level)
	}
	return slog.New(slog.NewMultiHandler(stdoutHandler(w, level), minLevel{
		Handler: otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(provider)),
		level:   exportLevel,
	}))
}

func stdoutHandler(w io.Writer, level slog.Level) slog.Handler {
	return traceHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})}
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// minLevel drops records below level before the wrapped handler builds them.
type minLevel struct {
	slog.Handler
	level slog.Level
}

func (h minLevel) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level && h.Handler.Enabled(ctx, l)
}

func (h minLevel) WithAttrs(attrs []slog.Attr) slog.Handler {
	return minLevel{h.Handler.WithAttrs(attrs), h.level}
}

func (h minLevel) WithGroup(name string) slog.Handler {
	return minLevel{h.Handler.WithGroup(name), h.level}
}
