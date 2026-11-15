// Package pyroscope controls optional CPU profiling with the Pyroscope SDK.
package pyroscope

import (
	"context"
	"errors"
	"fmt"
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
	sdkUploadWorkers   = 5
	sdkUploadTimeout   = 30 * time.Second
	sessionStopTimeout = 5 * time.Second
)

// ErrSessionActive reports a competing adapter session in this process.
var ErrSessionActive = errors.New("pyroscope: another adapter session is active")

// ErrCleanupPending reports an enable request before SDK cleanup finishes.
var ErrCleanupPending = errors.New("pyroscope: session cleanup is pending")

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
	cleanup  *cleanupOperation
	closing  bool
}

type cleanupOperation struct {
	done chan struct{}
	err  error
}

type cleanupPanicError struct{ message string }

func (e *cleanupPanicError) Error() string {
	return "pyroscope: SDK cleanup panicked; session ownership remains reserved: " + e.message
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
// Disable waits at most five seconds or the caller's earlier deadline.
// Cleanup continues after a timeout and rejects reenablement until it finishes.
//
// Example:
//
//	err := backend.SetEnabled(ctx, true)
func (b *Backend) SetEnabled(ctx context.Context, enabled bool) error {
	if enabled {
		return b.enable(ctx)
	}

	ctx, cancel := context.WithTimeout(ctx, sessionStopTimeout)
	defer cancel()

	b.mu.Lock()
	if b.status.Closed || b.closing {
		b.mu.Unlock()

		return telemetry.ErrClosed
	}

	if err := ctx.Err(); err != nil {
		b.mu.Unlock()

		return &operationError{operation: "update session", err: err}
	}

	operation := b.beginCleanup()
	b.mu.Unlock()

	return waitForCleanup(ctx, operation)
}

// Status returns the local session state and backend lifecycle.
//
// Status remains available while SDK cleanup waits for a foreground profile.
// Enabled reports local SDK session state, rather than remote upload receipt.
//
// Example:
//
//	status := backend.Status()
func (b *Backend) Status() telemetry.BackendStatus {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.status
}

// Close rejects updates and waits at most five seconds for SDK cleanup.
//
// A timed-out Close leaves cleanup running and permits a later Close retry.
// SDK shutdown does not guarantee delivery of pending uploads.
//
// Example:
//
//	err := backend.Close()
func (b *Backend) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), sessionStopTimeout)
	defer cancel()

	b.mu.Lock()
	b.closing = true
	operation := b.beginCleanup()
	b.mu.Unlock()

	return waitForCleanup(ctx, operation)
}

func (b *Backend) enable(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status.Closed || b.closing {
		return telemetry.ErrClosed
	}

	if err := ctx.Err(); err != nil {
		return &operationError{operation: "update session", err: err}
	}

	if b.cleanup != nil {
		return ErrCleanupPending
	}

	if b.status.Enabled {
		return nil
	}

	sessionOwner.Lock()
	if sessionOwner.backend != nil {
		sessionOwner.Unlock()

		return ErrSessionActive
	}

	sessionOwner.backend = b
	sessionOwner.Unlock()

	if b.session == nil {
		if err := b.prepare(); err != nil {
			b.releaseOwnership()

			return err
		}
	}

	if err := ctx.Err(); err != nil {
		b.releaseOwnership()

		return &operationError{operation: "start session", err: err}
	}

	if err := b.session.Start(); err != nil {
		b.releaseOwnership()

		return &operationError{operation: "start session", err: err}
	}

	b.uploader.Start()
	b.status.Enabled = true

	return nil
}

func (b *Backend) prepare() error {
	config := b.config
	if config.Remote.HTTPClient != nil {
		config.Remote.HTTPClient = &deadlineClient{
			next:    config.Remote.HTTPClient,
			timeout: config.Remote.Timeout,
		}
	}

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

func (b *Backend) beginCleanup() *cleanupOperation {
	if b.cleanup != nil {
		return b.cleanup
	}

	if !b.status.Enabled {
		if b.closing {
			b.session, b.uploader = nil, nil
			b.status.Closed = true
		}

		return nil
	}

	operation := &cleanupOperation{done: make(chan struct{})}
	b.cleanup = operation
	session, uploader := b.session, b.uploader
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				operation.err = &cleanupPanicError{message: fmt.Sprint(recovered)}
				close(operation.done)
			}
		}()
		b.finishCleanup(operation, session, uploader)
	}()

	return operation
}

func (b *Backend) finishCleanup(
	operation *cleanupOperation,
	session *sdk.Session,
	uploader *remote.Remote,
) {
	session.Stop()
	b.mu.Lock()
	b.status.Enabled = false
	b.mu.Unlock()

	uploader.Stop()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.session, b.uploader = nil, nil
	b.status.Closed = b.closing
	b.cleanup = nil
	b.releaseOwnership()
	close(operation.done)
}

func (b *Backend) releaseOwnership() {
	sessionOwner.Lock()
	defer sessionOwner.Unlock()
	sessionOwner.backend = nil
}

func waitForCleanup(ctx context.Context, operation *cleanupOperation) error {
	if operation == nil {
		return nil
	}

	select {
	case <-operation.done:
		return operation.err
	case <-ctx.Done():
		select {
		case <-operation.done:
			return operation.err
		default:
			return &operationError{operation: "wait for session cleanup", err: ctx.Err()}
		}
	}
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
