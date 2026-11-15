package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSetupDisabled(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	p, err := Setup(context.Background(), "test")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if p.Logs != nil {
		t.Errorf("Logs = %v, want nil when the SDK is disabled", p.Logs)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestShutdownAllRunsInParallel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	fastDone := make(chan time.Time, 2)
	fast := func(context.Context) error { fastDone <- time.Now(); return nil }
	slow := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() } // a trace flush that hangs

	start := time.Now()
	err := shutdownAll(ctx, slow, fast, fast)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the slow shutdown's deadline error", err)
	}
	if len(fastDone) != 2 {
		t.Fatalf("%d fast shutdowns ran, want 2", len(fastDone))
	}
	for range 2 {
		if d := (<-fastDone).Sub(start); d > 100*time.Millisecond {
			t.Errorf("a fast shutdown finished after %v, behind the slow one", d)
		}
	}
}
