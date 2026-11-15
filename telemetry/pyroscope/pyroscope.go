// Package pyroscope controls optional CPU profiling with the Pyroscope SDK.
package pyroscope

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"strings"
	"sync"
	"time"

	otelpyroscope "github.com/grafana/otel-profiling-go"
	sdk "github.com/grafana/pyroscope-go"
	"github.com/grafana/pyroscope-go/upstream/remote"
	oteltrace "go.opentelemetry.io/otel/trace"

	"goodkind.io/gklog/telemetry"
)

const (
	sdkUploadWorkers = 5
	sdkUploadTimeout = 30 * time.Second
)

// ErrSessionActive reports a competing adapter session in this process.
var ErrSessionActive = errors.New("pyroscope: another adapter session is active")

type configError struct{ message string }

func (e *configError) Error() string { return "pyroscope: " + e.message }

type operationError struct {
	operation string
	err       error
}

func (e *operationError) Error() string { return "pyroscope: " + e.operation + ": " + e.err.Error() }
func (e *operationError) Unwrap() error { return e.err }

var sessionOwner struct {
	sync.Mutex

	backend *Backend
}

// Options configures the supported Pyroscope startup settings.
type Options struct {
	ApplicationName string
	Tags            map[string]string
	UploadRate      time.Duration
	Remote          remote.Config
}

// Backend serializes CPU profiling updates and retains startup configuration.
type Backend struct {
	mu       sync.Mutex
	config   Options
	session  *sdk.Session
	uploader *remote.Remote
	status   telemetry.BackendStatus
}

// New validates options and creates a disabled CPU profiling backend.
//
// Remote.Logger must be non-nil. Retained maps do not share caller storage.
//
// Example:
//
//	options := Options{
//		ApplicationName: "service",
//		Remote: remote.Config{Address: endpoint, Logger: sdk.StandardLogger},
//	}
//	backend, err := New(options)
func New(config Options) (*Backend, error) {
	if config.Remote.Logger == nil {
		return nil, &configError{message: "logger is required"}
	}

	address, err := url.Parse(config.Remote.Address)
	if err != nil || address.Host == "" || (address.Scheme != "http" && address.Scheme != "https") {
		return nil, &configError{message: "server address must be an absolute HTTP or HTTPS URL"}
	}

	if strings.TrimSpace(config.ApplicationName) == "" {
		return nil, &configError{message: "application name is required"}
	}

	if err := validateApplicationName(config.ApplicationName); err != nil {
		return nil, err
	}

	if config.UploadRate < 0 {
		return nil, &configError{message: "upload rate must not be negative"}
	}

	if config.Remote.Threads < 0 {
		return nil, &configError{message: "upload worker count must not be negative"}
	}

	if config.Remote.Timeout < 0 {
		return nil, &configError{message: "upload timeout must not be negative"}
	}

	if config.Remote.Threads == 0 {
		config.Remote.Threads = sdkUploadWorkers
	}

	if config.Remote.Timeout == 0 {
		config.Remote.Timeout = sdkUploadTimeout
	}

	config.Tags = maps.Clone(config.Tags)
	config.Remote.HTTPHeaders = maps.Clone(config.Remote.HTTPHeaders)
	backend := &Backend{config: config}
	if err := backend.prepare(); err != nil {
		return nil, err
	}

	return backend, nil
}

// SetEnabled starts or stops one CPU session unless the backend has closed.
//
// Enabled reports a local session, rather than remote upload receipt.
//
// Example:
//
//	err := backend.SetEnabled(ctx, true)
func (b *Backend) SetEnabled(ctx context.Context, enabled bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status.Closed {
		return telemetry.ErrClosed
	}

	if err := ctx.Err(); err != nil {
		return &operationError{operation: "update session", err: err}
	}

	if b.status.Enabled == enabled {
		return nil
	}

	sessionOwner.Lock()
	defer sessionOwner.Unlock()
	if err := ctx.Err(); err != nil {
		return &operationError{operation: "update session", err: err}
	}

	if !enabled {
		b.stop()

		return nil
	}

	if sessionOwner.backend != nil {
		return ErrSessionActive
	}

	if b.session == nil {
		if err := b.prepare(); err != nil {
			return err
		}
	}

	if err := b.session.Start(); err != nil {
		return &operationError{operation: "start session", err: err}
	}

	b.uploader.Start()
	sessionOwner.backend = b
	b.status.Enabled = true

	return nil
}

// Status returns the local session state and backend lifecycle.
//
// Status serializes with session startup and shutdown.
//
// Example:
//
//	status := backend.Status()
func (b *Backend) Status() telemetry.BackendStatus {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.status
}

// Close stops the CPU session and rejects future updates.
//
// The SDK does not guarantee delivery of pending uploads during shutdown.
//
// Example:
//
//	err := backend.Close()
func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	sessionOwner.Lock()
	defer sessionOwner.Unlock()
	b.stop()
	b.session, b.uploader = nil, nil
	b.status.Closed = true

	return nil
}

func (b *Backend) prepare() error {
	config := b.config
	// Construct SDK components directly because sdk.Start rereads an endpoint environment variable.
	uploader, err := remote.NewRemote(config.Remote)
	if err != nil {
		return &operationError{operation: "create uploader", err: err}
	}

	session, err := sdk.NewSession(sdk.SessionConfig{
		Upstream:       uploader,
		Logger:         config.Remote.Logger,
		AppName:        config.ApplicationName,
		Tags:           config.Tags,
		ProfilingTypes: []sdk.ProfileType{sdk.ProfileCPU},
		UploadRate:     config.UploadRate,
	})
	if err != nil {
		return &operationError{operation: "create session", err: err}
	}

	b.session, b.uploader = session, uploader

	return nil
}

func (b *Backend) stop() {
	if !b.status.Enabled {
		return
	}

	b.session.Stop()
	b.uploader.Stop()
	b.session, b.uploader = nil, nil
	b.status.Enabled = false
	sessionOwner.backend = nil
}

// TracerProvider adds the SDK's default local root profile labels to spans.
type TracerProvider struct {
	oteltrace.TracerProvider
}

// WrapTracerProvider adds trace and profile correlation with SDK defaults.
//
// The caller retains ownership of the original provider and its shutdown.
//
// Example:
//
//	provider := WrapTracerProvider(sdkProvider)
func WrapTracerProvider(provider oteltrace.TracerProvider) *TracerProvider {
	return &TracerProvider{TracerProvider: otelpyroscope.NewTracerProvider(provider)}
}

func validateApplicationName(application string) error {
	_, tags, hasTags := strings.Cut(application, "{")
	if !hasTags {
		return nil
	}

	if !strings.HasSuffix(tags, "}") || strings.ContainsAny(strings.TrimSuffix(tags, "}"), "{}") {
		return &configError{message: "application tags require matching braces"}
	}

	tags = strings.TrimSuffix(tags, "}")
	if tags == "" {
		return nil
	}

	for tag := range strings.SplitSeq(tags, ",") {
		_, _, ok := strings.Cut(tag, "=")
		if !ok {
			return &configError{message: "application tags require key=value pairs"}
		}
	}

	return nil
}
