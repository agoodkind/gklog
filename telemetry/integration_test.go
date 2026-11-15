package telemetry_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	sdkprofile "github.com/grafana/pyroscope-go"
	"github.com/grafana/pyroscope-go/upstream/remote"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	oteltrace "go.opentelemetry.io/otel/trace"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	"goodkind.io/gklog"
	"goodkind.io/gklog/telemetry"
	"goodkind.io/gklog/telemetry/otlpmetrics"
	"goodkind.io/gklog/telemetry/pyroscope"
	gktrace "goodkind.io/gklog/trace"
)

type correlationTraces struct {
	collectortrace.UnimplementedTraceServiceServer
	requests chan *collectortrace.ExportTraceServiceRequest
}

func (r *correlationTraces) Export(
	_ context.Context,
	request *collectortrace.ExportTraceServiceRequest,
) (*collectortrace.ExportTraceServiceResponse, error) {
	r.requests <- request

	return &collectortrace.ExportTraceServiceResponse{}, nil
}

type correlationMetrics struct {
	collectormetric.UnimplementedMetricsServiceServer
	requests chan *collectormetric.ExportMetricsServiceRequest
}

func (r *correlationMetrics) Export(
	_ context.Context,
	request *collectormetric.ExportMetricsServiceRequest,
) (*collectormetric.ExportMetricsServiceResponse, error) {
	r.requests <- request

	return &collectormetric.ExportMetricsServiceResponse{}, nil
}

func correlationCollector(t *testing.T) (string, *correlationTraces, *correlationMetrics) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	traces := &correlationTraces{requests: make(chan *collectortrace.ExportTraceServiceRequest, 8)}
	metrics := &correlationMetrics{
		requests: make(chan *collectormetric.ExportMetricsServiceRequest, 8),
	}
	server := grpc.NewServer()
	collectortrace.RegisterTraceServiceServer(server, traces)
	collectormetric.RegisterMetricsServiceServer(server, metrics)
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-finished; err != nil {
			t.Error(err)
		}
	})

	return listener.Addr().String(), traces, metrics
}

type correlationProfile struct {
	profile *profile.Profile
	name    string
}

func correlationProfiler(t *testing.T) (string, <-chan correlationProfile) {
	t.Helper()
	uploads := make(chan correlationProfile, 128)
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			file, _, err := r.FormFile("profile")
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			defer func() {
				if err := file.Close(); err != nil {
					t.Error(err)
				}

				if err := r.MultipartForm.RemoveAll(); err != nil {
					t.Error(err)
				}
			}()
			parsed, err := profile.Parse(file)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			if err := parsed.CheckValid(); err != nil {
				t.Error(err)
			}

			uploads <- correlationProfile{profile: parsed, name: r.URL.Query().Get("name")}
			w.WriteHeader(http.StatusOK)
		}),
	)
	t.Cleanup(server.Close)

	return server.URL, uploads
}

func TestHTTPRequestCorrelatesTelemetry(t *testing.T) {
	previousTrace := otel.GetTracerProvider()
	previousMeter := otel.GetMeterProvider()
	previousPropagator := otel.GetTextMapPropagator()
	previousLogger := slog.Default()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTrace)
		otel.SetMeterProvider(previousMeter)
		otel.SetTextMapPropagator(previousPropagator)
		slog.SetDefault(previousLogger)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	endpoint, traces, metrics := correlationCollector(t)
	profileEndpoint, uploads := correlationProfiler(t)
	profileBackend, err := pyroscope.New(pyroscope.Options{
		ApplicationName: "request-test",
		UploadRate:      100 * time.Millisecond,
		Tags:            map[string]string{"service_name": "request-test"},
		Remote:          remote.Config{Address: profileEndpoint, Logger: sdkprofile.StandardLogger},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := profileBackend.Close(); err != nil {
			t.Error(err)
		}
	})
	profiles, err := telemetry.NewProfileControl(ctx, profileBackend, true)
	if err != nil {
		t.Fatal(err)
	}

	metricBackend, err := otlpmetrics.New(
		ctx,
		otlpmetrics.Options{
			Resource: resource.NewSchemaless(
				attribute.String("service.name", "request-test"),
			),
			ExporterOptions: []otlpmetricgrpc.Option{
				otlpmetricgrpc.WithEndpoint(endpoint),
				otlpmetricgrpc.WithInsecure(),
			},
			ReaderOptions: []sdkmetric.PeriodicReaderOption{sdkmetric.WithInterval(time.Hour)},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := metricBackend.Close(); err != nil {
			t.Error(err)
		}
	})
	metricControl, err := telemetry.NewMetricControl(ctx, metricBackend, true)
	if err != nil {
		t.Fatal(err)
	}

	histogram, err := metricBackend.MeterProvider().Meter("request-test").Float64Histogram(
		"request.work",
	)
	if err != nil {
		t.Fatal(err)
	}

	traceControl, err := telemetry.NewTraceControl(telemetry.TraceSettings{
		Enabled:     true,
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	traceCloser, err := gktrace.Setup(gktrace.Options{
		ServiceName: "request-test",
		Endpoint:    "http://" + endpoint,
		Sampler:     traceControl.Sampler(),
		WrapTracerProvider: func(provider oteltrace.TracerProvider) oteltrace.TracerProvider {
			return pyroscope.WrapTracerProvider(provider)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := traceCloser.Close(); err != nil {
			t.Error(err)
		}
	})
	logs, err := telemetry.NewLogControl(telemetry.LogSettings{
		Enabled: true,
		Level:   slog.LevelDebug,
	})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	slog.SetDefault(
		slog.New(
			logs.Handler(
				slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}),
			),
		),
	)
	var requestSpan oteltrace.SpanContext
	var captured correlationProfile
	handler := gktrace.RequestLogger(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestSpan = oteltrace.SpanContextFromContext(r.Context())
			gklog.L(r.Context()).DebugContext(r.Context(), "handler.work")
			histogram.Record(r.Context(), 42)
			captured = observeRequestProfile(t, r.Context(), uploads, requestSpan)
			w.WriteHeader(http.StatusAccepted)
		}),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/work", nil))
	if response.Code != http.StatusAccepted ||
		response.Header().Get(gktrace.RequestIDHeader) == "" {
		t.Fatalf("HTTP response status = %d, headers = %v", response.Code, response.Header())
	}

	if !profiles.Status().Backend.Enabled ||
		!strings.Contains(captured.name, "service_name=request-test") {
		t.Fatalf("active profile upload = %q, status = %+v", captured.name, profiles.Status())
	}

	if err := profiles.Close(); err != nil {
		t.Fatal(err)
	}

	if err := metricBackend.MeterProvider().ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}

	if err := traceCloser.Close(); err != nil {
		t.Fatal(err)
	}

	assertRequestExports(t, ctx, traces, metrics, output.Bytes(), requestSpan)

	for _, closer := range []interface{ Close() error }{metricControl, logs, traceControl} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if state := profiles.Status(); !state.Closed || !state.Backend.Closed || state.Backend.Enabled {
		t.Fatalf("profile cleanup = %+v", state)
	}

	if state := metricControl.Status(); !state.Closed || !state.Backend.Closed ||
		state.Backend.Enabled {
		t.Fatalf("metric cleanup = %+v", state)
	}
}

func observeRequestProfile(
	t *testing.T,
	ctx context.Context,
	uploads <-chan correlationProfile,
	span oteltrace.SpanContext,
) correlationProfile {
	t.Helper()

	for {
		select {
		case upload := <-uploads:
			for _, sample := range upload.profile.Sample {
				if upload.profile.PeriodType.Type == "cpu" &&
					upload.profile.PeriodType.Unit == "nanoseconds" &&
					slices.Contains(sample.Label["trace_id"], span.TraceID().String()) &&
					slices.Contains(sample.Label["span_id"], span.SpanID().String()) &&
					slices.Contains(sample.Label["span_name"], "GET /work") &&
					slices.ContainsFunc(sample.Value, func(value int64) bool { return value > 0 }) {
					return upload
				}
			}
		case <-ctx.Done():
			t.Fatal("Pyroscope did not upload CPU samples with the HTTP request IDs")

			return correlationProfile{}
		default:
			var value uint64

			for index := range 100000 {
				value = value*33 + uint64(index)
			}

			if value == 0 {
				t.Fatal("CPU workload did not run")
			}
		}
	}
}

func assertRequestExports(
	t *testing.T,
	ctx context.Context,
	traces *correlationTraces,
	metrics *correlationMetrics,
	output []byte,
	span oteltrace.SpanContext,
) {
	t.Helper()
	var record struct {
		Message string `json:"msg"`
		TraceID string `json:"trace_id"`
		SpanID  string `json:"span_id"`
	}

	if err := json.NewDecoder(bytes.NewReader(output)).Decode(&record); err != nil {
		t.Fatal(err)
	}

	if record.Message != "handler.work" || record.TraceID != span.TraceID().String() ||
		record.SpanID != span.SpanID().String() {
		t.Fatalf("diagnostic log = %+v", record)
	}

	select {
	case request := <-traces.requests:
		exported := request.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()[0]
		if hex.EncodeToString(exported.GetTraceId()) != record.TraceID ||
			hex.EncodeToString(
				exported.GetSpanId(),
			) != record.SpanID || exported.GetName() != "GET /work" {
			t.Fatalf("exported request span = %v", exported)
		}

		found := false

		for _, attr := range exported.GetAttributes() {
			if attr.GetKey() == "pyroscope.profile.id" &&
				attr.GetValue().GetStringValue() == record.SpanID {
				found = true
			}
		}

		if !found {
			t.Fatalf("request span did not include pyroscope.profile.id = %s", record.SpanID)
		}
	case <-ctx.Done():
		t.Fatal("OTLP receiver did not receive the request span")
	}

	select {
	case request := <-metrics.requests:
		point := request.GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics()[0].GetHistogram().GetDataPoints()[0]
		if point.GetCount() != 1 || point.GetSum() != 42 || len(point.GetExemplars()) != 1 {
			t.Fatalf("request metric = %v", point)
		}

		exemplar := point.GetExemplars()[0]
		if hex.EncodeToString(exemplar.GetTraceId()) != record.TraceID ||
			hex.EncodeToString(
				exemplar.GetSpanId(),
			) != record.SpanID || exemplar.GetAsDouble() != 42 {
			t.Fatalf("request metric exemplar = %v", exemplar)
		}
	case <-ctx.Done():
		t.Fatal("OTLP receiver did not receive the request metric")
	}
}
