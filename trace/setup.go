package trace

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// defaultInstrumentationName is used when Options.InstrumentationName is "".
const defaultInstrumentationName = "goodkind.io/gklog/trace"

// instrumentationName holds the active tracer name set by the most recent
// call to Setup. Tracer() reads it; defaults to defaultInstrumentationName.
var instrumentationName atomic.Value

// Options configures Setup. None of the fields read environment variables;
// callers wire their own config layer to populate this struct.
type Options struct {
	// ServiceName populates the OTel resource service.name attribute.
	// Required when Endpoint is non-empty.
	ServiceName string
	// ServiceNamespace populates service.namespace. Optional.
	ServiceNamespace string
	// InstrumentationName is the tracer name returned by Tracer(). Empty
	// defers to "goodkind.io/gklog/trace".
	InstrumentationName string
	// Endpoint is the OTLP gRPC endpoint. Empty disables export; spans
	// are still produced (so trace_id / span_id flow into logs) but
	// nothing is shipped off-process.
	Endpoint string
}

// Setup installs a TracerProvider and the W3C TraceContext + Baggage
// propagator on the global OTel registry. The returned io.Closer flushes
// and shuts the provider down; callers must Close on shutdown.
//
// Setup is safe to call once per process; calling it again replaces the
// global provider but does not shut the previous one down (the caller
// owns that lifecycle through the Closer it received earlier).
func Setup(opts Options) (io.Closer, error) {
	name := strings.TrimSpace(opts.InstrumentationName)
	if name == "" {
		name = defaultInstrumentationName
	}
	instrumentationName.Store(name)

	resourceAttrs, err := buildResource(opts)
	if err != nil {
		return nil, err
	}

	providerOptions := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resourceAttrs),
	}

	if endpoint := strings.TrimSpace(opts.Endpoint); endpoint != "" {
		exporter, err := newTraceExporter(endpoint)
		if err != nil {
			return nil, err
		}
		providerOptions = append(providerOptions, sdktrace.WithBatcher(exporter))
	}

	provider := sdktrace.NewTracerProvider(providerOptions...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return traceCloser{shutdowns: []func(context.Context) error{provider.Shutdown}}, nil
}

// Tracer returns the OTel tracer for the active instrumentation name.
func Tracer() trace.Tracer {
	name := defaultInstrumentationName
	if v, ok := instrumentationName.Load().(string); ok && v != "" {
		name = v
	}
	return otel.Tracer(name)
}

// StartSpan starts a child span using the active instrumentation name.
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

func buildResource(opts Options) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{}
	if name := strings.TrimSpace(opts.ServiceName); name != "" {
		attrs = append(attrs, semconv.ServiceName(name))
	}
	if ns := strings.TrimSpace(opts.ServiceNamespace); ns != "" {
		attrs = append(attrs, attribute.String("service.namespace", ns))
	}
	if len(attrs) == 0 {
		return resource.Default(), nil
	}
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, attrs...),
	)
}

func newTraceExporter(endpoint string) (*otlptrace.Exporter, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	options := []otlptracegrpc.Option{}
	if parsedURL, err := url.Parse(endpoint); err == nil && parsedURL.Host != "" {
		endpoint = parsedURL.Host
		if parsedURL.Scheme == "http" {
			options = append(options, otlptracegrpc.WithInsecure())
		}
	}

	return otlptracegrpc.New(ctx, append(options, otlptracegrpc.WithEndpoint(endpoint))...)
}

type traceCloser struct {
	shutdowns []func(context.Context) error
}

func (t traceCloser) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var errs []error
	for i := len(t.shutdowns) - 1; i >= 0; i-- {
		if err := t.shutdowns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
