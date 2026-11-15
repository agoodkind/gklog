package telemetry_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"goodkind.io/gklog/telemetry"
	"goodkind.io/gklog/telemetry/otlpmetrics"
)

func TestBackendControlsWithoutBackends(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	profile, err := telemetry.NewProfileControl(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	metrics, err := telemetry.NewMetricControl(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	controls := []struct {
		name   string
		update func(context.Context, bool) error
		status func() telemetry.BackendControlStatus
		close  func() error
	}{
		{name: "profile", update: profile.Update, status: profile.Status, close: profile.Close},
		{name: "metric", update: metrics.Update, status: metrics.Status, close: metrics.Close},
	}
	for _, control := range controls {
		t.Run(
			control.name,
			func(t *testing.T) {
				if status := control.status(); status.Available || status.Backend.Enabled ||
					status.Closed {
					t.Fatalf("missing backend status = %+v", status)
				}

				if err := control.update(
					ctx,
					true,
				); !errors.Is(err, telemetry.ErrBackendUnavailable) {
					t.Fatalf("enable missing backend = %v", err)
				}

				if err := control.update(ctx, false); err != nil {
					t.Fatal(err)
				}

				var workers sync.WaitGroup

				for range 4 {
					workers.Go(func() {
						for range 100 {
							_ = control.status()
							if err := control.update(ctx, false); err != nil &&
								!errors.Is(err, telemetry.ErrClosed) {
								t.Error(err)
							}
						}
					})
				}

				workers.Go(func() {
					if err := control.close(); err != nil {
						t.Error(err)
					}
				})
				workers.Wait()
				if status := control.status(); !status.Closed || status.Available ||
					status.Backend.Enabled {
					t.Fatalf("closed missing backend status = %+v", status)
				}

				if err := control.update(ctx, false); !errors.Is(err, telemetry.ErrClosed) {
					t.Fatalf("update after close = %v", err)
				}

				if err := control.close(); err != nil {
					t.Fatal(err)
				}
			},
		)
	}

	if _, err := telemetry.NewProfileControl(ctx, nil, true); !errors.Is(
		err,
		telemetry.ErrBackendUnavailable,
	) {
		t.Fatalf("profile constructor without backend = %v", err)
	}

	if _, err := telemetry.NewMetricControl(ctx, nil, true); !errors.Is(
		err,
		telemetry.ErrBackendUnavailable,
	) {
		t.Fatalf("metric constructor without backend = %v", err)
	}
}

func newMetricControlBackend(t *testing.T) *otlpmetrics.Backend {
	t.Helper()
	endpoint, _, _ := correlationCollector(t)
	backend, err := otlpmetrics.New(
		t.Context(),
		otlpmetrics.Options{
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
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})

	return backend
}

func TestMetricControlCloseAllowsObservableStatus(t *testing.T) {
	t.Parallel()
	backend := newMetricControlBackend(t)
	control, err := telemetry.NewMetricControl(t.Context(), backend, false)
	if err != nil {
		t.Fatal(err)
	}

	observed := make(chan telemetry.BackendControlStatus, 1)
	released := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	t.Cleanup(release)
	_, err = backend.MeterProvider().Meter("control-shutdown").Int64ObservableGauge(
		"control.state",
		metric.WithInt64Callback(func(ctx context.Context, observer metric.Int64Observer) error {
			observed <- control.Status()
			select {
			case <-released:
			case <-ctx.Done():
				return ctx.Err()
			}

			observer.Observe(1)

			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() { closed <- control.Close() }()
	select {
	case state := <-observed:
		if !state.Available || !state.Closed || state.Backend.Closed || state.Backend.Enabled {
			t.Fatalf("shutdown callback status = %+v", state)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("observable callback could not read the control status during shutdown")
	}

	if state := control.Status(); !state.Closed || state.Backend.Closed {
		t.Fatalf("status while cleanup waits for the callback = %+v", state)
	}

	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control close did not finish after the observable callback returned")
	}

	if state := control.Status(); !state.Closed || !state.Backend.Closed || state.Backend.Enabled {
		t.Fatalf("completed shutdown status = %+v", state)
	}

	if err := control.Update(t.Context(), true); !errors.Is(err, telemetry.ErrClosed) {
		t.Fatalf("update after callback shutdown = %v", err)
	}
}

func TestMetricControlConcurrentUpdateStatusAndClose(t *testing.T) {
	t.Parallel()
	backend := newMetricControlBackend(t)
	control, err := telemetry.NewMetricControl(t.Context(), backend, true)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := control.Update(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update = %v", err)
	}

	if state := control.Status(); !state.Backend.Enabled || state.Closed {
		t.Fatalf("canceled update changed backend state: %+v", state)
	}

	var workers sync.WaitGroup

	for range 4 {
		workers.Go(func() {
			for index := range 100 {
				if err := control.Update(t.Context(), index%2 == 0); err != nil &&
					!errors.Is(err, telemetry.ErrClosed) {
					t.Error(err)
				}

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
	if state := control.Status(); !state.Available || !state.Closed || !state.Backend.Closed ||
		state.Backend.Enabled {
		t.Fatalf("concurrent shutdown status = %+v", state)
	}

	if err := control.Update(ctx, false); !errors.Is(err, telemetry.ErrClosed) {
		t.Fatalf("canceled update after closure = %v", err)
	}

	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
}
