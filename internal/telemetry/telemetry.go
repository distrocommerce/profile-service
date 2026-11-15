// Package telemetry sets up OpenTelemetry tracing, metrics and log export and
// the structured logger.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// DefaultServiceName is used when OTEL_SERVICE_NAME is not set.
const DefaultServiceName = "profile"

// Providers is what Setup returns.
type Providers struct {
	// Logs exports log records over OTLP; pass it to NewExportingLogger.
	// Nil when the SDK is disabled.
	Logs *sdklog.LoggerProvider
	// Shutdown flushes and stops every provider, in parallel.
	Shutdown func(context.Context) error
}

// Setup installs global tracer and meter providers that export over OTLP/gRPC,
// and the W3C trace-context and baggage propagators, and returns a log provider
// that exports over OTLP/gRPC as well. Exporters, sampler and resource read the
// standard OTEL_* variables (OTEL_SERVICE_NAME, OTEL_EXPORTER_OTLP_ENDPOINT,
// OTEL_TRACES_SAMPLER, OTEL_RESOURCE_ATTRIBUTES, OTEL_BLRP_*, ...); an http://
// endpoint means a plaintext connection. OTEL_SDK_DISABLED=true leaves the no-op
// providers in place and returns no log provider.
//
// Go runtime metrics (memory, goroutines, GC goal, GOMAXPROCS, goroutine
// scheduling latency) are exported with the application metrics.
func Setup(ctx context.Context, version string) (Providers, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return Providers{Shutdown: func(context.Context) error { return nil }}, nil
	}

	// Later options win: OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES override the default name.
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(DefaultServiceName), semconv.ServiceVersion(version)),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil {
		return Providers{}, fmt.Errorf("build resource: %w", err)
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return Providers{}, fmt.Errorf("create trace exporter: %w", err)
	}
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		_ = traceExp.Shutdown(ctx)
		return Providers{}, fmt.Errorf("create metric exporter: %w", err)
	}
	logExp, err := otlploggrpc.New(ctx)
	if err != nil {
		_ = traceExp.Shutdown(ctx)
		_ = metricExp.Shutdown(ctx)
		return Providers{}, fmt.Errorf("create log exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second),
			sdkmetric.WithProducer(runtime.NewProducer()), // go.schedule.duration
		)),
		sdkmetric.WithResource(res),
	)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return Providers{}, errors.Join(fmt.Errorf("start runtime metrics: %w", err),
			shutdownAll(ctx, tp.Shutdown, mp.Shutdown, lp.Shutdown))
	}
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	return Providers{
		Logs: lp,
		Shutdown: func(ctx context.Context) error {
			return shutdownAll(ctx, tp.Shutdown, mp.Shutdown, lp.Shutdown)
		},
	}, nil
}

// shutdownAll runs every shutdown in parallel on ctx and joins their errors, so
// a flush that hangs until the deadline does not take the others' time.
func shutdownAll(ctx context.Context, fns ...func(context.Context) error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Go(func() { errs[i] = fn(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}
