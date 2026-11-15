package telemetry

import (
	"math"
	"strconv"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TraceSettings configures sampling for new root spans.
type TraceSettings struct {
	Enabled     bool
	SampleRatio float64
}

func (s TraceSettings) validate() error {
	if math.IsNaN(s.SampleRatio) || math.IsInf(s.SampleRatio, 0) || s.SampleRatio < 0 ||
		s.SampleRatio > 1 {
		return &settingsError{
			field: "trace sample ratio",
			value: strconv.FormatFloat(s.SampleRatio, 'g', -1, 64),
		}
	}

	return nil
}

// TraceStatus reports root sampling settings and the control lifecycle.
type TraceStatus struct {
	TraceSettings

	Closed bool
}

// TraceControl updates root sampling without replacing the SDK provider.
type TraceControl struct {
	mu     sync.RWMutex
	status TraceStatus
	root   sdktrace.Sampler
}

// NewTraceControl validates settings and creates a trace sampling control.
//
// Ratios must be finite numbers between zero and one, inclusive.
//
// Example:
//
//	control, err := NewTraceControl(TraceSettings{Enabled: true, SampleRatio: 1})
func NewTraceControl(settings TraceSettings) (*TraceControl, error) {
	if err := settings.validate(); err != nil {
		return nil, err
	}

	return &TraceControl{
		status: TraceStatus{TraceSettings: settings},
		root:   sdktrace.TraceIDRatioBased(settings.SampleRatio),
	}, nil
}

// Update validates settings and applies them to future roots.
//
// Closed controls reject updates.
//
// Example:
//
//	err := control.Update(TraceSettings{Enabled: true, SampleRatio: 0.1})
func (c *TraceControl) Update(settings TraceSettings) error {
	if err := settings.validate(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.status.Closed {
		return ErrClosed
	}

	c.status.TraceSettings = settings
	c.root = sdktrace.TraceIDRatioBased(settings.SampleRatio)

	return nil
}

// Status returns a consistent copy of the current control state.
//
// Later updates do not change a previously returned value.
//
// Example:
//
//	status := control.Status()
func (c *TraceControl) Status() TraceStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.status
}

// Sampler preserves valid parent decisions and uses runtime settings for roots.
//
// Multiple samplers from this control share its runtime settings.
//
// Example:
//
//	option := sdktrace.WithSampler(control.Sampler())
func (c *TraceControl) Sampler() *TraceSampler {
	return &TraceSampler{sampler: sdktrace.ParentBased(&rootSampler{control: c})}
}

// TraceSampler implements sdktrace.Sampler for a TraceControl.
// Create a sampler with TraceControl.Sampler.
type TraceSampler struct {
	sampler sdktrace.Sampler
}

// ShouldSample delegates the decision to the parent-based sampler.
//
// Valid parents determine child sampling even after updates or Close.
//
// Example:
//
//	result := sampler.ShouldSample(parameters)
func (s *TraceSampler) ShouldSample(
	parameters sdktrace.SamplingParameters,
) sdktrace.SamplingResult {
	return s.sampler.ShouldSample(parameters)
}

// Description returns the SDK sampler description.
//
// The description includes the dynamic root sampler and parent policies.
//
// Example:
//
//	description := sampler.Description()
func (s *TraceSampler) Description() string {
	return s.sampler.Description()
}

// Close disables new roots and rejects updates without stopping existing spans.
//
// The caller retains ownership of the SDK provider and its shutdown.
//
// Example:
//
//	err := control.Close()
func (c *TraceControl) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Enabled = false
	c.status.Closed = true

	return nil
}

type rootSampler struct {
	control *TraceControl
}

func (s *rootSampler) ShouldSample(parameters sdktrace.SamplingParameters) sdktrace.SamplingResult {
	s.control.mu.RLock()
	defer s.control.mu.RUnlock()

	if !s.control.status.Enabled || s.control.status.Closed {
		return sdktrace.NeverSample().ShouldSample(parameters)
	}

	return s.control.root.ShouldSample(parameters)
}

func (*rootSampler) Description() string {
	return "telemetry runtime root sampler"
}
