package otlpmetrics_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/gklog/telemetry"
	"goodkind.io/gklog/telemetry/otlpmetrics"
)

type metricReceiver struct {
	collectorpb.UnimplementedMetricsServiceServer
	requests chan *collectorpb.ExportMetricsServiceRequest
	block    atomic.Bool
	reject   atomic.Bool
	entered  chan struct{}
	release  chan struct{}
}

func (r *metricReceiver) Export(
	ctx context.Context,
	request *collectorpb.ExportMetricsServiceRequest,
) (*collectorpb.ExportMetricsServiceResponse, error) {
	r.requests <- request
	if r.reject.Load() {
		return nil, status.Error(codes.InvalidArgument, "receiver rejected metric batch")
	}

	if r.block.Load() {
		r.entered <- struct{}{}
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return &collectorpb.ExportMetricsServiceResponse{}, nil
}

func startMetricBackend(t *testing.T) (*otlpmetrics.Backend, *metricReceiver) {
	t.Helper()

	return startMetricBackendWithResource(
		t,
		resource.NewSchemaless(attribute.String("service.name", "adapter-test")),
	)
}

func startMetricBackendWithResource(
	t *testing.T,
	metricResource *resource.Resource,
) (*otlpmetrics.Backend, *metricReceiver) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	receiver := &metricReceiver{
		requests: make(chan *collectorpb.ExportMetricsServiceRequest, 128),
		entered:  make(chan struct{}, 128),
		release:  make(chan struct{}),
	}
	server := grpc.NewServer()
	collectorpb.RegisterMetricsServiceServer(server, receiver)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil {
			t.Error(serveErr)
		}
	}()
	t.Cleanup(server.Stop)
	backend, err := otlpmetrics.New(
		t.Context(),
		otlpmetrics.Options{
			Resource: metricResource,
			ExporterOptions: []otlpmetricgrpc.Option{
				otlpmetricgrpc.WithEndpoint(listener.Addr().String()),
				otlpmetricgrpc.WithInsecure(),
				otlpmetricgrpc.WithTemporalitySelector(
					func(sdkmetric.InstrumentKind) metricdata.Temporality {
						return metricdata.DeltaTemporality
					},
				),
			},
			ReaderOptions: []sdkmetric.PeriodicReaderOption{sdkmetric.WithInterval(time.Hour)},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if closeErr := backend.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})

	return backend, receiver
}

func flushMetrics(t *testing.T, backend *otlpmetrics.Backend) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := backend.MeterProvider().ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
}

func nextMetrics(t *testing.T, receiver *metricReceiver) *collectorpb.ExportMetricsServiceRequest {
	t.Helper()
	select {
	case request := <-receiver.requests:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not receive metrics")

		return nil
	}
}

func TestMetricExportLifecycle(t *testing.T) {
	t.Parallel()
	backend, receiver := startMetricBackend(t)
	provider := backend.MeterProvider()
	counter, err := provider.Meter("adapter-test").Int64Counter("jobs")
	if err != nil {
		t.Fatal(err)
	}

	counter.Add(t.Context(), 2)
	flushMetrics(t, backend)
	select {
	case <-receiver.requests:
		t.Fatal("disabled backend exported metrics")
	default:
	}

	if status := backend.Status(); status.Enabled || status.Closed {
		t.Fatalf("initial status = %+v", status)
	}

	for _, value := range []int64{3, 7} {
		if err := backend.SetEnabled(t.Context(), true); err != nil {
			t.Fatal(err)
		}

		counter.Add(t.Context(), value)
		flushMetrics(t, backend)
		request := nextMetrics(t, receiver)
		metrics := request.GetResourceMetrics()
		if len(metrics) != 1 ||
			metrics[0].GetResource().GetAttributes()[0].GetValue().GetStringValue() != "adapter-test" {
			t.Fatalf("resource metrics = %v", metrics)
		}

		scope := metrics[0].GetScopeMetrics()[0]
		sum := scope.GetMetrics()[0].GetSum()
		if scope.GetScope().GetName() != "adapter-test" ||
			scope.GetMetrics()[0].GetName() != "jobs" ||
			sum.GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA ||
			sum.GetDataPoints()[0].GetAsInt() != value {
			t.Fatalf("exported metric = %v", scope)
		}

		if err := backend.SetEnabled(t.Context(), false); err != nil {
			t.Fatal(err)
		}

		counter.Add(t.Context(), 5)
		flushMetrics(t, backend)
		select {
		case <-receiver.requests:
			t.Fatal("disabled backend exported metrics")
		default:
		}
	}

	if backend.MeterProvider() != provider {
		t.Fatal("updates replaced the provider")
	}

	if err := backend.SetEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	counter.Add(t.Context(), 9)

	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-receiver.requests:
		t.Fatal("close exported the shutdown batch")
	default:
	}

	if status := backend.Status(); status.Enabled || !status.Closed {
		t.Fatalf("closed status = %+v", status)
	}

	if err := backend.SetEnabled(t.Context(), true); !errors.Is(err, telemetry.ErrClosed) {
		t.Fatalf("closed update = %v", err)
	}
}

func TestDisableWaitsForExportAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	backend, receiver := startMetricBackend(t)
	counter, err := backend.MeterProvider().Meter("wait").Int64Counter("jobs")
	if err != nil {
		t.Fatal(err)
	}

	if err := backend.SetEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	receiver.block.Store(true)
	t.Cleanup(func() { close(receiver.release) })
	counter.Add(t.Context(), 1)
	flushed := make(chan error, 1)
	go func() { flushed <- backend.MeterProvider().ForceFlush(t.Context()) }()
	select {
	case <-receiver.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not enter the receiver")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := backend.SetEnabled(ctx, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled disable = %v", err)
	}

	if !backend.Status().Enabled {
		t.Fatal("canceled disable changed enablement")
	}

	disabled := make(chan error, 1)
	go func() { disabled <- backend.SetEnabled(t.Context(), false) }()
	select {
	case err := <-disabled:
		t.Fatalf("disable returned before export completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	receiver.release <- struct{}{}
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}

	if err := <-disabled; err != nil {
		t.Fatal(err)
	}

	if backend.Status().Enabled {
		t.Fatal("successful disable left export enabled")
	}

	ctx, cancelCanceled := context.WithCancel(t.Context())
	cancelCanceled()
	if err := backend.SetEnabled(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled update = %v", err)
	}
}

func TestMetricExportPreservesHistogramExemplars(t *testing.T) {
	t.Parallel()
	backend, receiver := startMetricBackend(t)
	histogram, err := backend.MeterProvider().Meter("histogram").Float64Histogram("latency")
	if err != nil {
		t.Fatal(err)
	}

	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() {
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	})
	ctx, span := provider.Tracer("test").Start(t.Context(), "record")
	histogram.Record(ctx, 12.5)
	span.End()
	if err := backend.SetEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	flushMetrics(t, backend)
	point := nextMetrics(
		t,
		receiver,
	).GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics()[0].GetHistogram().GetDataPoints()[0]
	if point.GetCount() != 1 || point.GetSum() != 12.5 || len(point.GetExemplars()) != 1 {
		t.Fatalf("histogram = %v", point)
	}

	exemplar := point.GetExemplars()[0]
	traceID := span.SpanContext().TraceID()
	spanID := span.SpanContext().SpanID()
	if !bytes.Equal(exemplar.GetTraceId(), traceID[:]) ||
		!bytes.Equal(exemplar.GetSpanId(), spanID[:]) ||
		exemplar.GetAsDouble() != 12.5 ||
		len(point.GetAttributes()) != 0 {
		t.Fatalf("exemplar correlation = %v", point)
	}
}

func TestMetricConcurrentUpdatesAndStatus(t *testing.T) {
	t.Parallel()
	backend, _ := startMetricBackend(t)
	var workers sync.WaitGroup

	for range 4 {
		workers.Go(func() {
			for index := range 100 {
				if err := backend.SetEnabled(t.Context(), index%2 == 0); err != nil &&
					!errors.Is(err, telemetry.ErrClosed) {
					t.Error(err)
				}

				_ = backend.Status()
			}
		})
	}

	workers.Go(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	workers.Wait()
}

func TestMetricExportReturnsReceiverFailure(t *testing.T) {
	t.Parallel()
	backend, receiver := startMetricBackend(t)
	counter, err := backend.MeterProvider().Meter("failure").Int64Counter("jobs")
	if err != nil {
		t.Fatal(err)
	}

	if err := backend.SetEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	receiver.reject.Store(true)
	counter.Add(t.Context(), 1)
	if err := backend.MeterProvider().ForceFlush(t.Context()); status.Code(
		err,
	) != codes.InvalidArgument {
		t.Fatalf("export failure = %v", err)
	}

	if !backend.Status().Enabled {
		t.Fatal("receiver failure changed local enablement")
	}

	_ = nextMetrics(t, receiver)
	receiver.reject.Store(false)
	counter.Add(t.Context(), 1)
	flushMetrics(t, backend)
	_ = nextMetrics(t, receiver)
}

func TestMetricCloseDeadlineAndRetry(t *testing.T) {
	t.Parallel()
	backend, receiver := startMetricBackend(t)
	counter, err := backend.MeterProvider().Meter("shutdown").Int64Counter("jobs")
	if err != nil {
		t.Fatal(err)
	}

	if err := backend.SetEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	receiver.block.Store(true)
	t.Cleanup(func() { close(receiver.release) })
	counter.Add(t.Context(), 1)
	flushed := make(chan error, 1)
	go func() { flushed <- backend.MeterProvider().ForceFlush(t.Context()) }()
	select {
	case <-receiver.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not enter the receiver")
	}

	started := time.Now()
	if err := backend.Close(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked close = %v", err)
	}

	if elapsed := time.Since(started); elapsed < 5*time.Second || elapsed > 7*time.Second {
		t.Fatalf("close duration = %s, want five second deadline", elapsed)
	}

	if state := backend.Status(); !state.Enabled || state.Closed {
		t.Fatalf("failed close status = %+v", state)
	}

	receiver.release <- struct{}{}
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}

	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	if state := backend.Status(); state.Enabled || !state.Closed {
		t.Fatalf("retried close status = %+v", state)
	}
}

func TestMetricExportPreservesDefaultResource(t *testing.T) {
	t.Parallel()
	backend, receiver := startMetricBackendWithResource(t, nil)
	counter, err := backend.MeterProvider().Meter("defaults").Int64Counter("jobs")
	if err != nil {
		t.Fatal(err)
	}

	if err := backend.SetEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	counter.Add(t.Context(), 1)
	flushMetrics(t, backend)
	attributes := nextMetrics(t, receiver).GetResourceMetrics()[0].GetResource().GetAttributes()
	actual := make(map[string]string)

	for _, item := range attributes {
		actual[item.GetKey()] = item.GetValue().GetStringValue()
	}

	expected := resource.Default().Set()

	for _, key := range []attribute.Key{
		"service.name",
		"telemetry.sdk.name",
		"telemetry.sdk.language",
	} {
		value, exists := expected.Value(key)
		if !exists || actual[string(key)] != value.AsString() {
			t.Errorf(
				"default resource %s = %q, want %q",
				key,
				actual[string(key)],
				value.AsString(),
			)
		}
	}
}
