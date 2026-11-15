package telemetry

import (
	"io"
	"log/slog"

	"go.opentelemetry.io/otel"
)

// SetErrorLogger makes OpenTelemetry report its own errors (failed exports and
// the like) to w as JSON warn lines, at a fixed warn level so that LOG_LEVEL
// cannot hide them. They never go through an exporting logger: OTel's default
// handler calls log.Print, which slog.SetDefault routes into the exporting
// logger, so errors about exporting logs would be exported again.
func SetErrorLogger(w io.Writer) {
	otel.SetErrorHandler(errorHandler(NewLogger(w, slog.LevelWarn)))
}

func errorHandler(l *slog.Logger) otel.ErrorHandler {
	return otel.ErrorHandlerFunc(func(err error) {
		l.Warn("opentelemetry error", "error", err)
	})
}
