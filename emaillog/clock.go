// Clock helper for the emaillog package. Tests replace nowFn to
// freeze time for cooldown assertions.

package emaillog

import "time"

// nowFn returns the current wall-clock time. Tests may swap it out to
// drive deterministic cooldown behaviour. Production callers go
// through this indirection so that direct [time.Now] usage is
// confined to a single, swappable seam.
var nowFn = time.Now
