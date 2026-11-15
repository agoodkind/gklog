package telemetry_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	sdkprofile "github.com/grafana/pyroscope-go"
	"github.com/grafana/pyroscope-go/upstream/remote"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	oteltrace "go.opentelemetry.io/otel/trace"

	"goodkind.io/gklog"
	"goodkind.io/gklog/telemetry"
	"goodkind.io/gklog/telemetry/otlpmetrics"
	"goodkind.io/gklog/telemetry/pyroscope"
	gktrace "goodkind.io/gklog/trace"
)

func Example() {
	if err := serveWithTelemetry(nil, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

// serveWithTelemetry accepts optional application backends instead of the SDK adapters.
func serveWithTelemetry(profileBackend, metricBackend telemetry.Backend) (result error) {
	ctx := context.Background()
	logSettings, err := telemetry.LogSettingsFromEnv()
	if err != nil {
		return err
	}

	traceSettings, err := telemetry.TraceSettingsFromEnv()
	if err != nil {
		return err
	}

	profileEnabled, err := telemetry.ProfileEnabledFromEnv()
	if err != nil {
		return err
	}

	metricEnabled, err := telemetry.MetricExportEnabledFromEnv()
	if err != nil {
		return err
	}

	// Set OTEL_SERVICE_NAME and service.instance.id in OTEL_RESOURCE_ATTRIBUTES before startup.
	identity := resource.Default().Set()
	service, hasService := identity.Value("service.name")
	instance, hasInstance := identity.Value("service.instance.id")
	if !hasService || !hasInstance || service.AsString() == "" || instance.AsString() == "" {
		return errors.New("configure OTEL_SERVICE_NAME and service.instance.id before startup")
	}

	profileEndpoint := os.Getenv("PYROSCOPE_SERVER_ADDRESS")
	metricEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	traceEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if profileBackend == nil && profileEndpoint != "" {
		backend, err := pyroscope.New(pyroscope.Options{
			ApplicationName: service.AsString(),
			Tags: map[string]string{
				"service_name":        service.AsString(),
				"service_instance_id": instance.AsString(),
			},
			Remote: remote.Config{
				Address: profileEndpoint,
				Logger:  sdkprofile.StandardLogger,
			},
		})
		if err != nil {
			return err
		}

		profileBackend = backend
	}

	if profileBackend != nil {
		defer func() {
			if !profileBackend.Status().Closed {
				result = errors.Join(result, profileBackend.Close())
			}
		}()
	}

	if metricBackend == nil && metricEndpoint != "" {
		backend, err := otlpmetrics.New(
			ctx,
			otlpmetrics.Options{
				Resource: resource.Default(),
				ExporterOptions: []otlpmetricgrpc.Option{
					otlpmetricgrpc.WithEndpointURL(metricEndpoint),
				},
			},
		)
		if err != nil {
			return err
		}

		metricBackend = backend
		previousMeter := otel.GetMeterProvider()
		otel.SetMeterProvider(backend.MeterProvider())
		defer otel.SetMeterProvider(previousMeter)
	}

	if metricBackend != nil {
		defer func() {
			if !metricBackend.Status().Closed {
				result = errors.Join(result, metricBackend.Close())
			}
		}()
	}

	logs, err := telemetry.NewLogControl(logSettings)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, logs.Close()) }()
	traces, err := telemetry.NewTraceControl(traceSettings)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, traces.Close()) }()
	profiles, err := telemetry.NewProfileControl(ctx, profileBackend, profileEnabled)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, profiles.Close()) }()
	metrics, err := telemetry.NewMetricControl(ctx, metricBackend, metricEnabled)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, metrics.Close()) }()
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	defer otel.SetTracerProvider(previousProvider)
	defer otel.SetTextMapPropagator(previousPropagator)
	closer, err := gktrace.Setup(gktrace.Options{
		ServiceName: service.AsString(),
		Endpoint:    traceEndpoint,
		Sampler:     traces.Sampler(),
		WrapTracerProvider: func(provider oteltrace.TracerProvider) oteltrace.TracerProvider {
			return pyroscope.WrapTracerProvider(provider)
		},
	})
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, closer.Close()) }()
	diagnostic := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(logs.Handler(diagnostic)).With(
		slog.String("service.name", service.AsString()),
		slog.String("service.instance.id", instance.AsString()),
	)
	previousLogger := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(previousLogger)
	// Required audit output uses a separate handler outside logs.Handler.
	audit := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(
		slog.String("service.name", service.AsString()),
		slog.String("service.instance.id", instance.AsString()),
	)
	audit.InfoContext(ctx, "service.start")
	counter, err := otel.Meter("example").Int64Counter("requests")
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              ":8080",
		ReadHeaderTimeout: 5 * time.Second,
		Handler: gktrace.RequestLogger(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gklog.L(r.Context()).DebugContext(r.Context(), "handler.work")
				counter.Add(r.Context(), 1)
				w.WriteHeader(http.StatusNoContent)
			}),
		),
	}

	return server.ListenAndServe()
}
