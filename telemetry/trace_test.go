package telemetry_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"goodkind.io/gklog/telemetry"
)

func TestTraceControlPreservesParentsAcrossUpdatesAndClose(t *testing.T) {
	t.Parallel()
	control, err := telemetry.NewTraceControl(telemetry.TraceSettings{
		Enabled:     true,
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(control.Sampler()),
		sdktrace.WithSyncer(exporter),
	)
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	tracer := provider.Tracer("runtime-control-test")
	sampledContext, sampled := tracer.Start(context.Background(), "sampled root")
	if !sampled.IsRecording() || !sampled.SpanContext().IsSampled() {
		t.Fatal("enabled root did not record")
	}

	if err := control.Update(telemetry.TraceSettings{
		Enabled:     false,
		SampleRatio: 1,
	}); err != nil {
		t.Fatal(err)
	}

	unsampledContext, unsampled := tracer.Start(context.Background(), "disabled root")
	if unsampled.IsRecording() || unsampled.SpanContext().IsSampled() {
		t.Fatal("disabled root recorded")
	}

	_, sampledChild := tracer.Start(sampledContext, "sampled child while disabled")
	if !sampledChild.IsRecording() || !sampledChild.SpanContext().IsSampled() {
		t.Fatal("disable changed a sampled parent decision")
	}

	sampledChild.End()
	if err := control.Update(telemetry.TraceSettings{
		Enabled:     true,
		SampleRatio: 1,
	}); err != nil {
		t.Fatal(err)
	}

	_, unsampledChild := tracer.Start(unsampledContext, "unsampled child while enabled")
	if unsampledChild.IsRecording() || unsampledChild.SpanContext().IsSampled() {
		t.Fatal("enable changed an unsampled parent decision")
	}

	unsampledChild.End()
	if err := control.Update(telemetry.TraceSettings{
		Enabled:     true,
		SampleRatio: 0,
	}); err != nil {
		t.Fatal(err)
	}

	_, zeroRatio := tracer.Start(context.Background(), "zero ratio root")
	if zeroRatio.IsRecording() || zeroRatio.SpanContext().IsSampled() {
		t.Fatal("zero ratio root recorded")
	}

	zeroRatio.End()
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}

	_, closedRoot := tracer.Start(context.Background(), "closed root")
	if closedRoot.IsRecording() {
		t.Fatal("closed control recorded a new root")
	}

	closedRoot.End()
	_, finalChild := tracer.Start(sampledContext, "sampled child after close")
	if !finalChild.IsRecording() {
		t.Fatal("control close stopped an existing sampled trace")
	}

	finalChild.End()
	sampled.End()
	unsampled.End()
	if got := len(exporter.GetSpans()); got != 3 {
		t.Fatalf("exported spans = %d, want 3", got)
	}

	if err := control.Update(telemetry.TraceSettings{
		Enabled:     true,
		SampleRatio: 1,
	}); !errors.Is(err, telemetry.ErrClosed) {
		t.Fatalf("update after close = %v", err)
	}

	if status := control.Status(); !status.Closed || status.Enabled {
		t.Fatalf("closed status = %+v", status)
	}
}

func TestTraceControlHonorsLocalAndRemoteParentDecisions(t *testing.T) {
	t.Parallel()

	for _, remote := range []bool{false, true} {
		for _, sampled := range []bool{false, true} {
			control, err := telemetry.NewTraceControl(telemetry.TraceSettings{
				Enabled:     !sampled,
				SampleRatio: 1,
			})
			if err != nil {
				t.Fatal(err)
			}

			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(control.Sampler()))
			flags := oteltrace.TraceFlags(0)
			if sampled {
				flags = oteltrace.FlagsSampled
			}

			parent := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
				TraceID: oteltrace.TraceID{
					1,
				},
				SpanID:     oteltrace.SpanID{1},
				TraceFlags: flags,
				Remote:     remote,
			})
			ctx := oteltrace.ContextWithSpanContext(context.Background(), parent)
			_, span := provider.Tracer("parent-test").Start(ctx, "child")
			if span.SpanContext().IsSampled() != sampled || span.IsRecording() != sampled {
				t.Errorf(
					"remote=%v parent sampled=%v: child sampled=%v recording=%v",
					remote,
					sampled,
					span.SpanContext().IsSampled(),
					span.IsRecording(),
				)
			}

			if span.SpanContext().TraceID() != parent.TraceID() {
				t.Error("child changed the parent trace ID")
			}

			span.End()
			if err := provider.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTraceControlRejectsInvalidRatios(t *testing.T) {
	t.Parallel()
	control, err := telemetry.NewTraceControl(telemetry.TraceSettings{
		Enabled:     true,
		SampleRatio: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}

	before := control.Status()

	for _, ratio := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.1, 1.1} {
		if _, err := telemetry.NewTraceControl(telemetry.TraceSettings{
			SampleRatio: ratio,
		}); err == nil {
			t.Errorf("constructor accepted ratio %v", ratio)
		}

		if err := control.Update(telemetry.TraceSettings{SampleRatio: ratio}); err == nil {
			t.Errorf("update accepted ratio %v", ratio)
		}

		if after := control.Status(); after != before {
			t.Fatalf("invalid update changed status: %+v", after)
		}
	}
}

func TestTraceControlConcurrentUpdatesStatusAndSpans(t *testing.T) {
	t.Parallel()
	control, err := telemetry.NewTraceControl(telemetry.TraceSettings{
		Enabled:     true,
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(control.Sampler()))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	tracer := provider.Tracer("concurrent-test")
	var workers sync.WaitGroup

	for range 4 {
		workers.Go(func() {
			for index := range 100 {
				if err := control.Update(telemetry.TraceSettings{
					Enabled:     index%2 == 0,
					SampleRatio: 1,
				}); err != nil &&
					!errors.Is(err, telemetry.ErrClosed) {
					t.Error(err)
				}

				ctx, root := tracer.Start(context.Background(), "root")
				_, child := tracer.Start(ctx, "child")
				if root.SpanContext().IsSampled() != child.SpanContext().IsSampled() {
					t.Error("concurrent update changed a parent decision")
				}

				child.End()
				root.End()
				_ = control.Status()
			}
		})
	}

	workers.Go(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	workers.Wait()
}
