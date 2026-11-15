package telemetry_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"goodkind.io/gklog/telemetry"
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
