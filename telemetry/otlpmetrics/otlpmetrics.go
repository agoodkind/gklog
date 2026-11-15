// Package otlpmetrics controls optional OTLP metric export over gRPC.
package otlpmetrics

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"

	"goodkind.io/gklog/telemetry"
)

const shutdownTimeout = 5 * time.Second

// Options configures the metric provider, gRPC exporter, and periodic reader.
type Options struct {
	Resource        *resource.Resource
	ExporterOptions []otlpmetricgrpc.Option
	ReaderOptions   []sdkmetric.PeriodicReaderOption
}

// Backend preserves its provider and instruments across export updates.
type Backend struct {
	gate     chan struct{}
	mu       sync.RWMutex
	status   telemetry.BackendStatus
	provider *sdkmetric.MeterProvider
	closeMu  sync.Mutex
	closeErr error
	closing  bool
}

// New creates a metric provider that collects while export starts disabled.
//
// Exporter and reader options retain the SDK transport and collection settings.
//
// Example:
//
//	backend, err := New(ctx, Options{Resource: resource})
func New(ctx context.Context, options Options) (*Backend, error) {
	exporter, err := otlpmetricgrpc.New(ctx, options.ExporterOptions...)
	if err != nil {
		return nil, &operationError{operation: "create exporter", err: err}
	}

	backend := &Backend{gate: make(chan struct{}, 1)}
	reader := sdkmetric.NewPeriodicReader(
		&gatedExporter{Exporter: exporter, backend: backend},
		options.ReaderOptions...,
	)
	providerOptions := []sdkmetric.Option{sdkmetric.WithReader(reader)}
	if options.Resource != nil {
		providerOptions = append(providerOptions, sdkmetric.WithResource(options.Resource))
	}

	backend.provider = sdkmetric.NewMeterProvider(providerOptions...)

	return backend, nil
}

// MeterProvider returns the stable provider for application instruments.
//
// Backend.Close owns provider shutdown.
//
// Example:
//
//	meter := backend.MeterProvider().Meter("service")
func (b *Backend) MeterProvider() *sdkmetric.MeterProvider {
	return b.provider
}

// SetEnabled updates export after any active send completes.
//
// A canceled update leaves enablement unchanged.
//
// Example:
//
//	err := backend.SetEnabled(ctx, false)
func (b *Backend) SetEnabled(ctx context.Context, enabled bool) error {
	if err := b.acquire(ctx); err != nil {
		return err
	}

	defer func() { <-b.gate }()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status.Closed || b.closing {
		return telemetry.ErrClosed
	}

	if err := ctx.Err(); err != nil {
		return &operationError{operation: "update export", err: err}
	}

	b.status.Enabled = enabled

	return nil
}

// Status reports export enablement and provider lifecycle.
//
// Status remains available while the exporter waits for a receiver.
//
// Example:
//
//	status := backend.Status()
func (b *Backend) Status() telemetry.BackendStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.status
}

// Close disables export before shutting down the provider.
//
// Shutdown uses a five second deadline and later calls return its result.
//
// Example:
//
//	err := backend.Close()
func (b *Backend) Close() error {
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	if b.Status().Closed {
		return b.closeErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := b.acquire(ctx); err != nil {
		return err
	}

	b.mu.Lock()
	b.status.Enabled = false
	b.closing = true
	b.mu.Unlock()
	<-b.gate
	b.closeErr = b.provider.Shutdown(ctx)
	b.mu.Lock()
	b.status.Closed = true
	b.closing = false
	b.mu.Unlock()

	return b.closeErr
}

type gatedExporter struct {
	*otlpmetricgrpc.Exporter

	backend *Backend
}

func (b *Backend) acquire(ctx context.Context) error {
	select {
	case b.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-b.gate

			return &operationError{operation: "wait for exporter", err: err}
		}

		return nil
	case <-ctx.Done():
		return &operationError{operation: "wait for exporter", err: ctx.Err()}
	}
}

func (e *gatedExporter) Export(ctx context.Context, metrics *metricdata.ResourceMetrics) error {
	if err := e.backend.acquire(ctx); err != nil {
		return err
	}

	defer func() { <-e.backend.gate }()
	if !e.backend.Status().Enabled {
		return nil
	}

	if err := e.Exporter.Export(ctx, metrics); err != nil {
		return &operationError{operation: "export metrics", err: err}
	}

	return nil
}

type operationError struct {
	operation string
	err       error
}

func (e *operationError) Error() string { return "otlpmetrics: " + e.operation + ": " + e.err.Error() }
func (e *operationError) Unwrap() error { return e.err }
