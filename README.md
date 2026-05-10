# gklog

Structured logging helpers around `log/slog`: tee handlers, optional JSON file rotation, optional email alerts, build metadata.

## Context scoped loggers

Store a request scoped `*slog.Logger` on `context.Context` so middleware can attach fields once (for example `request_id`) and downstream code logs without threading a logger parameter.

- `gklog.WithLogger(ctx, log)` stores `log` on `ctx` (or returns `ctx` unchanged when `log` is nil).
- `gklog.LoggerFromContext(ctx)` returns that logger, or `slog.Default()` when none was stored.
- `gklog.L(ctx)` is a short alias for `LoggerFromContext`.

Downstream should prefer `LoggerFromContext(ctx).InfoContext(ctx, msg, ...)` so the record carries the same `context` through `slog` handlers.

## Build metadata

Consumers must provide valid gklog build metadata through their normal build pipeline. Builds that do not provide valid metadata should fail before application startup.

## Releasing

After merging changes, tag a new version (or let consumers pin a pseudo version with `go get goodkind.io/gklog@<commit>`). Downstream modules update their `require` and drop any temporary `replace` directives pointing at a local checkout.
