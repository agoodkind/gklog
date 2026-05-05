// Clock helper for the trace package. Tests replace nowFn to drive
// deterministic latency assertions for HTTP middleware, the Op timer,
// and the pgx tracer.

package trace

import "time"

// nowFn returns the current wall-clock time. Tests may swap it out to
// freeze time across middleware, Op, and the pgx tracer.
var nowFn = time.Now
