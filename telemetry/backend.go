package telemetry

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed reports an update after a control has closed.
var ErrClosed = errors.New("telemetry: control is closed")

// ErrBackendUnavailable reports an enable request without a backend.
var ErrBackendUnavailable = errors.New("telemetry: backend is unavailable")

// Backend applies profiling or metric export settings and reports actual state.
// Implementations must make Status safe during other backend calls.
type Backend interface {
	SetEnabled(ctx context.Context, enabled bool) error
	Status() BackendStatus
	Close() error
}

// BackendStatus reports the actual state of a backend after its operations.
type BackendStatus struct {
	Enabled bool
	Closed  bool
}

// BackendControlStatus reports backend availability and actual state.
// Closed reports the control lifecycle. Backend.Closed reports backend cleanup.
// A failed Close can leave Closed true while Backend.Enabled remains true.
type BackendControlStatus struct {
	Available bool
	Closed    bool
	Backend   BackendStatus
}

// ProfileControl independently updates an optional profiling backend.
type ProfileControl struct {
	backend backendControl
}

// NewProfileControl applies the initial setting to backend.
//
// Enabling profiling requires a backend.
//
// Example:
//
//	control, err := NewProfileControl(ctx, backend, false)
func NewProfileControl(
	ctx context.Context,
	backend Backend,
	enabled bool,
) (*ProfileControl, error) {
	control := &ProfileControl{backend: backendControl{backend: backend}}
	if err := control.Update(ctx, enabled); err != nil {
		return nil, err
	}

	return control, nil
}

// Update applies enabled to the backend unless the control has closed.
//
// Enabling without a backend returns ErrBackendUnavailable.
//
// Example:
//
//	err := control.Update(ctx, true)
func (c *ProfileControl) Update(ctx context.Context, enabled bool) error {
	return c.backend.update(ctx, enabled)
}

// Status reports backend availability, actual state, and the control lifecycle.
//
// Backend state comes directly from Backend.Status.
//
// Example:
//
//	status := control.Status()
func (c *ProfileControl) Status() BackendControlStatus {
	return c.backend.status()
}

// Close rejects future updates and closes the backend.
//
// A later Close retries cleanup if the backend remains open after a failure.
//
// Example:
//
//	err := control.Close()
func (c *ProfileControl) Close() error {
	return c.backend.close()
}

// MetricControl independently updates an optional metric export backend.
type MetricControl struct {
	backend backendControl
}

// NewMetricControl applies the initial setting to backend.
//
// Enabling metric export requires a backend.
//
// Example:
//
//	control, err := NewMetricControl(ctx, backend, false)
func NewMetricControl(ctx context.Context, backend Backend, enabled bool) (*MetricControl, error) {
	control := &MetricControl{backend: backendControl{backend: backend}}
	if err := control.Update(ctx, enabled); err != nil {
		return nil, err
	}

	return control, nil
}

// Update applies enabled to the backend unless the control has closed.
//
// Enabling without a backend returns ErrBackendUnavailable.
//
// Example:
//
//	err := control.Update(ctx, true)
func (c *MetricControl) Update(ctx context.Context, enabled bool) error {
	return c.backend.update(ctx, enabled)
}

// Status reports backend availability, actual state, and the control lifecycle.
//
// Backend state comes directly from Backend.Status.
//
// Example:
//
//	status := control.Status()
func (c *MetricControl) Status() BackendControlStatus {
	return c.backend.status()
}

// Close rejects future updates and closes the backend.
//
// A later Close retries cleanup if the backend remains open after a failure.
//
// Example:
//
//	err := control.Close()
func (c *MetricControl) Close() error {
	return c.backend.close()
}

type backendControl struct {
	mu      sync.Mutex
	backend Backend
	closed  bool
}

func (c *backendControl) update(ctx context.Context, enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClosed
	}

	if c.backend == nil {
		if enabled {
			return ErrBackendUnavailable
		}

		return nil
	}

	if err := c.backend.SetEnabled(ctx, enabled); err != nil {
		return &operationError{operation: "update backend", err: err}
	}

	return nil
}

func (c *backendControl) status() BackendControlStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := BackendControlStatus{Available: c.backend != nil, Closed: c.closed}
	if c.backend != nil {
		status.Backend = c.backend.Status()
	}

	return status
}

func (c *backendControl) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true

	if c.backend == nil || c.backend.Status().Closed {
		return nil
	}

	if err := c.backend.Close(); err != nil {
		return &operationError{operation: "close backend", err: err}
	}

	return nil
}

type settingsError struct {
	field string
	value string
}

func (e *settingsError) Error() string {
	return "telemetry: invalid " + e.field + ": " + e.value
}

type operationError struct {
	operation string
	err       error
}

func (e *operationError) Error() string {
	return "telemetry: " + e.operation + ": " + e.err.Error()
}

func (e *operationError) Unwrap() error { return e.err }
