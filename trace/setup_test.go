package trace_test

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"goodkind.io/gklog/trace"
)

func TestSetupInstallsSamplerAndWrappedProvider(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	var original oteltrace.TracerProvider
	var wrapped *providerWrapper
	closer, err := trace.Setup(trace.Options{
		Sampler: sdktrace.NeverSample(),
		WrapTracerProvider: func(provider oteltrace.TracerProvider) oteltrace.TracerProvider {
			original = provider
			wrapped = &providerWrapper{TracerProvider: provider}

			return wrapped
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Error(err)
		}
	})
	if otel.GetTracerProvider() != wrapped {
		t.Fatal("Setup did not install the wrapped provider")
	}

	_, span := trace.StartSpan(context.Background(), "unsampled root")
	if span.IsRecording() || span.SpanContext().IsSampled() || !span.SpanContext().IsValid() {
		t.Fatalf(
			"custom sampler span context = %v, recording = %v",
			span.SpanContext(),
			span.IsRecording(),
		)
	}

	span.End()
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	if original == nil {
		t.Fatal("wrapper did not receive the SDK provider")
	}
}

func TestSetupCloserShutsDownOriginalSDKProvider(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	var original oteltrace.TracerProvider
	closer, err := trace.Setup(trace.Options{
		WrapTracerProvider: func(provider oteltrace.TracerProvider) oteltrace.TracerProvider {
			original = provider

			return &providerWrapper{TracerProvider: provider}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Error(err)
		}
	})
	tracer := original.Tracer("shutdown-test")
	_, before := tracer.Start(context.Background(), "before close")
	if !before.IsRecording() {
		t.Fatal("default sampler did not record before close")
	}

	before.End()
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	_, after := original.Tracer("after-shutdown-test").Start(context.Background(), "after close")
	if after.IsRecording() {
		t.Fatal("returned closer did not shut down the SDK provider")
	}

	after.End()
}

func TestSetupRejectsNilWrapperAndShutsDownSDKProvider(t *testing.T) {
	previous := otel.GetTracerProvider()
	var original oteltrace.TracerProvider
	closer, err := trace.Setup(trace.Options{
		WrapTracerProvider: func(provider oteltrace.TracerProvider) oteltrace.TracerProvider {
			original = provider

			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "nil tracer provider") || closer != nil {
		t.Fatalf("nil wrapper result = %v, %v", closer, err)
	}

	if otel.GetTracerProvider() != previous {
		t.Fatal("failed Setup replaced the global provider")
	}

	if original == nil {
		t.Fatal("wrapper did not receive the SDK provider")
	}

	_, span := original.Tracer("failed-setup-test").Start(
		context.Background(),
		"after failed setup",
	)
	if span.IsRecording() {
		t.Fatal("failed Setup did not shut down the SDK provider")
	}

	span.End()
}

type providerWrapper struct {
	oteltrace.TracerProvider
}
