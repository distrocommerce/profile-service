// Package telemetry sets up OpenTelemetry tracing and metrics and the
// structured logger.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// DefaultServiceName is used when OTEL_SERVICE_NAME is not set.
const DefaultServiceName = "profile"

// Setup installs global tracer and meter providers that export over OTLP/gRPC,
// and the W3C trace-context and baggage propagators. Exporters, sampler and
// resource read the standard OTEL_* variables (OTEL_SERVICE_NAME,
// OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_TRACES_SAMPLER, OTEL_RESOURCE_ATTRIBUTES,
// ...); an http:// endpoint means a plaintext connection.
// OTEL_SDK_DISABLED=true leaves the no-op providers in place.
//
// The returned function flushes and stops the providers.
func Setup(ctx context.Context, version string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return func(context.Context) error { return nil }, nil
	}

	// Later options win: OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES override the default name.
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(DefaultServiceName), semconv.ServiceVersion(version)),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		_ = traceExp.Shutdown(ctx)
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}
