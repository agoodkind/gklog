package telemetry_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"goodkind.io/gklog/telemetry"
)

func clearTelemetryEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"GKLOG_LOG_ENABLED",
		"GKLOG_LOG_LEVEL",
		"GKLOG_TRACE_ENABLED",
		"GKLOG_TRACE_SAMPLE_RATIO",
		"GKLOG_CPU_PROFILE_ENABLED",
		"GKLOG_METRIC_EXPORT_ENABLED",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnvironmentDefaults(t *testing.T) {
	clearTelemetryEnv(t)
	logs, err := telemetry.LogSettingsFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if logs != (telemetry.LogSettings{Enabled: true, Level: slog.LevelInfo}) {
		t.Fatalf("default log settings = %+v", logs)
	}

	traces, err := telemetry.TraceSettingsFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if traces != (telemetry.TraceSettings{Enabled: true, SampleRatio: 1}) {
		t.Fatalf("default trace settings = %+v", traces)
	}

	if enabled, err := telemetry.ProfileEnabledFromEnv(); err != nil || enabled {
		t.Fatalf("default profile setting = %v, %v", enabled, err)
	}

	if enabled, err := telemetry.MetricExportEnabledFromEnv(); err != nil || enabled {
		t.Fatalf("default metric export setting = %v, %v", enabled, err)
	}
}

func TestEnvironmentStartupSettingsRemainExplicit(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("GKLOG_LOG_ENABLED", "true")
	t.Setenv("GKLOG_LOG_LEVEL", "WARN")
	t.Setenv("GKLOG_TRACE_ENABLED", "false")
	t.Setenv("GKLOG_TRACE_SAMPLE_RATIO", "0.25")
	t.Setenv("GKLOG_CPU_PROFILE_ENABLED", "true")
	t.Setenv("GKLOG_METRIC_EXPORT_ENABLED", "true")
	logSettings, err := telemetry.LogSettingsFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if logSettings != (telemetry.LogSettings{Enabled: true, Level: slog.LevelWarn}) {
		t.Fatalf("startup log settings = %+v", logSettings)
	}

	logs, err := telemetry.NewLogControl(logSettings)
	if err != nil {
		t.Fatal(err)
	}

	traceSettings, err := telemetry.TraceSettingsFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if traceSettings != (telemetry.TraceSettings{Enabled: false, SampleRatio: 0.25}) {
		t.Fatalf("startup trace settings = %+v", traceSettings)
	}

	traces, err := telemetry.NewTraceControl(traceSettings)
	if err != nil {
		t.Fatal(err)
	}

	if enabled, err := telemetry.ProfileEnabledFromEnv(); err != nil || !enabled {
		t.Fatalf("startup profile setting = %v, %v", enabled, err)
	}

	if enabled, err := telemetry.MetricExportEnabledFromEnv(); err != nil || !enabled {
		t.Fatalf("startup metric export setting = %v, %v", enabled, err)
	}

	t.Setenv("GKLOG_LOG_ENABLED", "invalid")
	t.Setenv("GKLOG_LOG_LEVEL", "invalid")
	t.Setenv("GKLOG_TRACE_ENABLED", "invalid")
	t.Setenv("GKLOG_TRACE_SAMPLE_RATIO", "invalid")
	var output bytes.Buffer
	logger := slog.New(logs.Handler(slog.NewJSONHandler(&output, nil)))
	logger.Info("below startup level")
	logger.Warn("startup level")
	if strings.Contains(output.String(), "below startup level") ||
		!strings.Contains(output.String(), "startup level") {
		t.Fatalf("logging reread the environment: %s", output.String())
	}

	if err := logs.Update(telemetry.LogSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	logger.Info("typed update")
	if !strings.Contains(output.String(), "typed update") {
		t.Fatalf("typed logging update did not apply: %s", output.String())
	}

	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(traces.Sampler()))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	tracer := provider.Tracer("environment-test")
	_, disabled := tracer.Start(context.Background(), "startup disabled")
	if disabled.IsRecording() {
		t.Fatal("tracing reread the environment")
	}

	disabled.End()
	if err := traces.Update(telemetry.TraceSettings{
		Enabled:     true,
		SampleRatio: 1,
	}); err != nil {
		t.Fatal(err)
	}

	_, enabled := tracer.Start(context.Background(), "typed update")
	if !enabled.IsRecording() {
		t.Fatal("typed trace update did not apply")
	}

	enabled.End()
}

func TestEnvironmentRejectsExplicitInvalidAndEmptyValues(t *testing.T) {
	clearTelemetryEnv(t)

	tests := []struct {
		key    string
		values []string
		read   func() error
	}{
		{
			key:    "GKLOG_LOG_ENABLED",
			values: []string{"", "sometimes"},
			read: func() error {
				_, err := telemetry.LogSettingsFromEnv()

				return err
			},
		},
		{
			key:    "GKLOG_LOG_LEVEL",
			values: []string{"", "verbose", "INFO+1", "ERROR+4"},
			read: func() error {
				_, err := telemetry.LogSettingsFromEnv()

				return err
			},
		},
		{
			key:    "GKLOG_TRACE_ENABLED",
			values: []string{"", "sometimes"},
			read: func() error {
				_, err := telemetry.TraceSettingsFromEnv()

				return err
			},
		},
		{
			key:    "GKLOG_TRACE_SAMPLE_RATIO",
			values: []string{"", "NaN", "+Inf", "-Inf", "-0.1", "1.1", "full"},
			read: func() error {
				_, err := telemetry.TraceSettingsFromEnv()

				return err
			},
		},
		{
			key:    "GKLOG_CPU_PROFILE_ENABLED",
			values: []string{"", "sometimes"},
			read: func() error {
				_, err := telemetry.ProfileEnabledFromEnv()

				return err
			},
		},
		{
			key:    "GKLOG_METRIC_EXPORT_ENABLED",
			values: []string{"", "sometimes"},
			read: func() error {
				_, err := telemetry.MetricExportEnabledFromEnv()

				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(
			test.key,
			func(t *testing.T) {
				for _, value := range test.values {
					t.Run(
						value,
						func(t *testing.T) {
							t.Setenv(test.key, value)
							if err := test.read(); err == nil || !strings.Contains(
								err.Error(),
								test.key,
							) {
								t.Fatalf("%s=%q error = %v", test.key, value, err)
							}
						},
					)
				}
			},
		)
	}
}
