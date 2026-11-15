package pyroscope_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	sdkpprof "github.com/grafana/pyroscope-go/http/pprof"
	"github.com/grafana/pyroscope-go/upstream/remote"

	"goodkind.io/gklog/telemetry"
	"goodkind.io/gklog/telemetry/pyroscope"
)

func TestForegroundProfileCleanup(t *testing.T) {
	for _, closeBackend := range []bool{false, true} {
		name := "disable"
		if closeBackend {
			name = "close"
		}

		t.Run(
			name,
			func(t *testing.T) {
				server, uploads := profileReceiver(t)
				options := pyroscope.Options{
					ApplicationName: "foreground",
					UploadRate:      100 * time.Millisecond,
					Remote: remote.Config{
						Address:    server.URL + "/startup",
						HTTPClient: server.Client(),
						Timeout:    100 * time.Millisecond,
					},
				}
				backend := newProfileBackend(t, options)
				options.ApplicationName = "after-foreground"
				competitor := newProfileBackend(t, options)
				cancelProfile := startForegroundProfile(t)
				if err := backend.SetEnabled(t.Context(), true); err != nil {
					t.Fatal(err)
				}

				waitForSDKProfileWait(
					t,
					"github.com/grafana/pyroscope-go/internal/pprof.SetCollector(",
					"(*cpuProfileCollector).Start(",
				)

				finished := make(chan error, 1)
				started := time.Now()
				go func() {
					if closeBackend {
						finished <- backend.Close()

						return
					}

					ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
					defer cancel()
					finished <- backend.SetEnabled(ctx, false)
				}()
				select {
				case err := <-finished:
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("blocked cleanup = %v", err)
					}
				case <-time.After(6 * time.Second):
					t.Fatal("foreground profiling blocked cleanup beyond its deadline")
				}

				elapsed := time.Since(started)
				if closeBackend && elapsed < 5*time.Second {
					t.Fatalf("close waited %s, want the five-second bound", elapsed)
				}

				if !closeBackend && elapsed > time.Second {
					t.Fatalf("disable waited %s, exceeding the caller's 50 ms deadline", elapsed)
				}

				assertPendingProfileState(t, backend)
				if closeBackend {
					if err := backend.SetEnabled(t.Context(), true); !errors.Is(
						err,
						telemetry.ErrClosed,
					) {
						t.Fatalf("update after timed-out close = %v", err)
					}
				} else {
					if err := backend.SetEnabled(t.Context(), true); !errors.Is(
						err,
						pyroscope.ErrCleanupPending,
					) {
						t.Fatalf("enable during cleanup = %v", err)
					}

					for range 3 {
						ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
						err := backend.SetEnabled(ctx, false)
						cancel()
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("disable retry = %v", err)
						}
					}
				}

				competing := make(chan error, 1)
				go func() { competing <- competitor.SetEnabled(t.Context(), true) }()
				select {
				case err := <-competing:
					if !errors.Is(err, pyroscope.ErrSessionActive) {
						t.Fatalf("competing enable during cleanup = %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("pending cleanup locked the process session owner")
				}

				retries := make(chan error, 3)

				for range 3 {
					go func() {
						if closeBackend {
							retries <- backend.Close()

							return
						}

						retries <- backend.SetEnabled(t.Context(), false)
					}()
				}

				select {
				case err := <-retries:
					t.Fatalf("cleanup completed before the foreground request ended: %v", err)
				case <-time.After(50 * time.Millisecond):
				}

				cancelProfile()

				for range 3 {
					select {
					case err := <-retries:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("cleanup retry did not complete after foreground cancellation")
					}
				}

				if state := backend.Status(); state.Enabled || state.Closed != closeBackend {
					t.Fatalf("completed cleanup state = %+v", state)
				}

				if err := competitor.SetEnabled(t.Context(), true); err != nil {
					t.Fatal(err)
				}

				for {
					upload := awaitCPUProfile(t, uploads)
					if strings.HasPrefix(upload.name, "after-foreground{") {
						break
					}
				}
			},
		)
	}
}

func assertPendingProfileState(t *testing.T, backend *pyroscope.Backend) {
	t.Helper()
	states := make(chan telemetry.BackendStatus, 1)
	go func() { states <- backend.Status() }()
	select {
	case state := <-states:
		if !state.Enabled || state.Closed {
			t.Fatalf("pending cleanup state = %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground cleanup blocked status")
	}
}

func startForegroundProfile(t *testing.T) context.CancelFunc {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(sdkpprof.Profile))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"?seconds=3600", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, requestErr := server.Client().Do(request)
		if requestErr != nil {
			if ctx.Err() == nil {
				t.Error(requestErr)
			}

			return
		}

		defer func() {
			if closeErr := response.Body.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}()
		if response.StatusCode != http.StatusOK {
			t.Errorf("foreground response status = %s", response.Status)
		}

		if _, readErr := io.Copy(io.Discard, response.Body); readErr != nil && ctx.Err() == nil {
			t.Error(readErr)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("foreground request did not stop after cancellation")
		}
	})
	waitForSDKProfileWait(t, "github.com/grafana/pyroscope-go/http/pprof.collectCPUProfile(", "")

	return cancel
}

func waitForSDKProfileWait(t *testing.T, topFunction, caller string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		var stacks bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
			t.Fatal(err)
		}

		for stack := range strings.SplitSeq(stacks.String(), "\n\n") {
			header, frames, _ := strings.Cut(stack, "\n")
			if strings.Contains(header, "[chan receive") &&
				strings.HasPrefix(frames, topFunction) &&
				strings.Contains(frames, caller) {
				return
			}
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("SDK did not wait in %s", topFunction)
		}
	}
}
