// Package clock centralizes wall-clock access for deterministic tests.
package clock

import "time"

// Clock returns the current time.
type Clock interface {
	Now() time.Time
}

// Func adapts a function to [Clock].
type Func func() time.Time

// Now returns the current time from f.
func (f Func) Now() time.Time {
	return f()
}

// System is the production wall-clock source.
var System Clock = Func(time.Now)
