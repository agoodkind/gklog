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
	"sync"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	sdk "github.com/grafana/pyroscope-go"
	"github.com/grafana/pyroscope-go/upstream/remote"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"goodkind.io/gklog/telemetry"
	"goodkind.io/gklog/telemetry/pyroscope"
)

type profileUpload struct {
	profile *profile.Profile
	name    string
	header  string
	tenant  string
	authed  bool
}

func profileReceiver(t *testing.T) (*httptest.Server, <-chan profileUpload) {
	t.Helper()
	uploads := make(chan profileUpload, 128)
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/startup/ingest" {
				t.Errorf("profile upload path = %s", request.URL.Path)
			}

			file, _, err := request.FormFile("profile")
			if err != nil {
				t.Error(err)
				response.WriteHeader(http.StatusBadRequest)

				return
			}

			defer func() {
				if err := file.Close(); err != nil {
					t.Error(err)
				}

				if request.MultipartForm != nil {
					if err := request.MultipartForm.RemoveAll(); err != nil {
						t.Error(err)
					}
				}
			}()
			parsed, err := profile.Parse(file)
			if err != nil {
				t.Error(err)
				response.WriteHeader(http.StatusBadRequest)

				return
			}

			if err := parsed.CheckValid(); err != nil {
				t.Error(err)
			}

			user, password, ok := request.BasicAuth()
			uploads <- profileUpload{
				profile: parsed,
				name:    request.URL.Query().Get("name"),
				header:  request.Header.Get("X-Adapter"),
				tenant:  request.Header.Get("X-Scope-Orgid"),
				authed:  ok && user == "test-user" && password == "example-password",
			}
			response.WriteHeader(http.StatusOK)
		}),
	)
	t.Cleanup(server.Close)

	return server, uploads
}

func newProfileBackend(t *testing.T, config pyroscope.Options) *pyroscope.Backend {
	t.Helper()
	config.Remote.Logger = sdk.StandardLogger
	backend, err := pyroscope.New(config)
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

func awaitCPUProfile(t *testing.T, uploads <-chan profileUpload) profileUpload {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	workers.Go(func() {
		for ctx.Err() == nil {
			var value uint64

			for index := range 100000 {
				value = value*33 + uint64(index)
			}

			if value == 0 {
				t.Error("CPU workload did not run")
			}
		}
	})
	defer workers.Wait()
	defer cancel()

	for {
		select {
		case upload := <-uploads:
			if upload.profile.PeriodType.Type != "cpu" ||
				upload.profile.PeriodType.Unit != "nanoseconds" {
				t.Fatalf("profile type = %v", upload.profile.PeriodType)
			}

			if len(upload.profile.Sample) != 0 {
				return upload
			}
		case <-ctx.Done():
			t.Fatal("receiver did not receive CPU samples")

			return profileUpload{}
		}
	}
}

func TestCPUProfileLifecycleAndRetainedConfiguration(t *testing.T) {
	server, uploads := profileReceiver(t)
	config := pyroscope.Options{
		ApplicationName: "adapter-test",
		UploadRate:      100 * time.Millisecond,
		Tags: map[string]string{
			"environment": "startup",
		},
		Remote: remote.Config{
			Address:           server.URL + "/startup",
			HTTPHeaders:       map[string]string{"X-Adapter": "startup"},
			BasicAuthUser:     "test-user",
			BasicAuthPassword: "example-password",
			TenantID:          "startup-tenant",
			HTTPClient:        server.Client(),
		},
	}
	backend := newProfileBackend(t, config)
	config.Tags["environment"] = "changed"
	config.Remote.HTTPHeaders["X-Adapter"] = "changed"
	t.Setenv("PYROSCOPE_ADHOC_SERVER_ADDRESS", "http://127.0.0.1:1")
	if status := backend.Status(); status.Enabled || status.Closed {
		t.Fatalf("initial status = %+v", status)
	}

	competitor := newProfileBackend(
		t,
		pyroscope.Options{
			ApplicationName: "competitor",
			UploadRate:      100 * time.Millisecond,
			Remote: remote.Config{
				Address:    server.URL + "/startup",
				HTTPClient: server.Client(),
			},
		},
	)

	for range 2 {
		if err := backend.SetEnabled(t.Context(), true); err != nil {
			t.Fatal(err)
		}

		if err := backend.SetEnabled(t.Context(), true); err != nil {
			t.Fatal(err)
		}

		if err := competitor.SetEnabled(t.Context(), true); !errors.Is(
			err,
			pyroscope.ErrSessionActive,
		) {
			t.Fatalf("competing enable = %v", err)
		}

		upload := awaitCPUProfile(t, uploads)
		if !strings.Contains(upload.name, "environment=startup") || upload.header != "startup" ||
			!upload.authed ||
			upload.tenant != "startup-tenant" {
			t.Fatalf("retained upload configuration = %+v", upload)
		}

		if err := backend.SetEnabled(t.Context(), false); err != nil {
			t.Fatal(err)
		}

		if err := backend.SetEnabled(t.Context(), false); err != nil {
			t.Fatal(err)
		}

		if backend.Status().Enabled {
			t.Fatal("disabled backend reports an active session")
		}

		var buffer bytes.Buffer
		if err := pprof.StartCPUProfile(&buffer); err != nil {
			t.Fatalf("adapter did not release runtime CPU profiling: %v", err)
		}

		pprof.StopCPUProfile()
		drainUploads(uploads)
	}

	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	if err := backend.SetEnabled(t.Context(), true); !errors.Is(err, telemetry.ErrClosed) {
		t.Fatalf("closed update = %v", err)
	}

	if status := backend.Status(); status.Enabled || !status.Closed {
		t.Fatalf("closed status = %+v", status)
	}

	if err := competitor.SetEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	for {
		upload := awaitCPUProfile(t, uploads)
		if strings.HasPrefix(upload.name, "competitor{") {
			break
		}
	}
}

func drainUploads(uploads <-chan profileUpload) {
	for {
		select {
		case <-uploads:
		default:
			return
		}
	}
}

func TestProfileRejectsInvalidStartupConfiguration(t *testing.T) {
	for _, config := range []pyroscope.Options{
		{Remote: remote.Config{Address: "http://localhost:4040"}},
		{ApplicationName: "invalid{", Remote: remote.Config{Address: "http://localhost:4040"}},
		{ApplicationName: "valid"},
		{ApplicationName: "valid", Remote: remote.Config{Address: "localhost:4040"}},
		{ApplicationName: "valid", Remote: remote.Config{Address: "ftp://localhost:4040"}},
		{
			ApplicationName: "valid",
			Remote:          remote.Config{Address: "http://localhost:4040"},
			UploadRate:      -time.Second,
		},
		{
			ApplicationName: "valid",
			Remote:          remote.Config{Address: "http://localhost:4040", Threads: -1},
		},
		{
			ApplicationName: "valid",
			Remote:          remote.Config{Address: "http://localhost:4040", Timeout: -time.Second},
		},
		{
			ApplicationName: "valid",
			Remote:          remote.Config{Address: "http://localhost:4040"},
			Tags:            map[string]string{"invalid-tag": "value"},
		},
	} {
		config.Remote.Logger = sdk.StandardLogger
		if backend, err := pyroscope.New(config); err == nil {
			_ = backend.Close()
			t.Errorf("constructor accepted invalid config: %+v", config)
		}
	}

	if _, err := pyroscope.New(pyroscope.Options{
		ApplicationName: "valid",
		Remote:          remote.Config{Address: "http://localhost:4040"},
	}); err == nil {
		t.Fatal("constructor accepted a missing logger")
	}
}

func TestProfileConcurrentUpdatesStatusAndCancellation(t *testing.T) {
	server, _ := profileReceiver(t)
	backend := newProfileBackend(
		t,
		pyroscope.Options{
			ApplicationName: "concurrent",
			Remote: remote.Config{
				Address:    server.URL + "/startup",
				HTTPClient: server.Client(),
			},
		},
	)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := backend.SetEnabled(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled enable = %v", err)
	}

	if backend.Status().Enabled {
		t.Fatal("canceled enable started profiling")
	}

	var workers sync.WaitGroup

	for range 4 {
		workers.Go(func() {
			for index := range 4 {
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

func TestTracerProviderPreservesLocalRootLabels(t *testing.T) {
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() {
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	})
	wrapped := pyroscope.WrapTracerProvider(provider)
	ctx, root := wrapped.Tracer("test").Start(t.Context(), "root")
	defer root.End()
	childContext, child := wrapped.Tracer("test").Start(ctx, "child")
	defer child.End()

	for key, expected := range map[string]string{
		"span_id":   root.SpanContext().SpanID().String(),
		"trace_id":  root.SpanContext().TraceID().String(),
		"span_name": "root",
	} {
		if actual, ok := pprof.Label(childContext, key); !ok || actual != expected {
			t.Errorf("child label %s = %q, want %q", key, actual, expected)
		}
	}
}

func TestProfileSuppliedClientUploadDeadline(t *testing.T) {
	for _, test := range []struct {
		name        string
		sendHeaders bool
		close       bool
	}{
		{name: "disable before response"},
		{name: "close during response body", sendHeaders: true, close: true},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				entered := make(chan struct{}, 8)
				canceled := make(chan struct{}, 8)
				release := make(chan struct{})
				server := httptest.NewTLSServer(
					http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
						if _, err := io.Copy(io.Discard, request.Body); err != nil {
							t.Error(err)

							return
						}

						if test.sendHeaders {
							response.Header().Set("Content-Length", "128")
							response.WriteHeader(http.StatusOK)
							if err := http.NewResponseController(response).Flush(); err != nil {
								t.Error(err)

								return
							}
						}

						entered <- struct{}{}
						select {
						case <-request.Context().Done():
							canceled <- struct{}{}
						case <-release:
						}
					}),
				)
				t.Cleanup(server.Close)
				client := server.Client()
				if client.Timeout != 0 {
					t.Fatalf("supplied client timeout = %s, want zero", client.Timeout)
				}

				backend := newProfileBackend(
					t,
					pyroscope.Options{
						ApplicationName: "upload-deadline",
						UploadRate:      100 * time.Millisecond,
						Remote: remote.Config{
							Address:    server.URL,
							HTTPClient: client,
							Threads:    1,
							Timeout:    500 * time.Millisecond,
						},
					},
				)
				t.Cleanup(func() { close(release) })
				if err := backend.SetEnabled(t.Context(), true); err != nil {
					t.Fatal(err)
				}

				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("receiver did not accept a profile upload")
				}

				finished := make(chan error, 1)
				go func() {
					if test.close {
						finished <- backend.Close()

						return
					}

					finished <- backend.SetEnabled(t.Context(), false)
				}()
				select {
				case err := <-finished:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("profiling cleanup exceeded the upload deadline")
				}

				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("upload deadline did not cancel the receiver request")
				}

				if state := backend.Status(); state.Enabled || state.Closed != test.close {
					t.Fatalf("cleanup status = %+v", state)
				}

				if client.Timeout != 0 {
					t.Fatalf("adapter changed supplied client timeout to %s", client.Timeout)
				}
			},
		)
	}
}
