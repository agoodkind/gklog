# gklog

Structured logging helpers around `log/slog`: tee handlers, optional JSON file rotation, optional email alerts, build metadata.

## Context scoped loggers

Store a request scoped `*slog.Logger` on `context.Context` so middleware can attach fields once (for example `request_id`) and downstream code logs without threading a logger parameter.

- `gklog.WithLogger(ctx, log)` stores `log` on `ctx` (or returns `ctx` unchanged when `log` is nil).
- `gklog.LoggerFromContext(ctx)` returns that logger, or `slog.Default()` when none was stored.
- `gklog.L(ctx)` is a short alias for `LoggerFromContext`.

Downstream should prefer `LoggerFromContext(ctx).InfoContext(ctx, msg, ...)` so the record carries the same `context` through `slog` handlers.

## Runtime telemetry

`telemetry` controls diagnostic logging, root trace sampling, CPU profiling, and metric export independently. The [composition example](telemetry/example_test.go) reads each control's environment settings once at startup and accepts optional `telemetry.Backend` replacements for the Pyroscope and OTLP metric adapters. Backend endpoints and credentials remain startup configuration.

Set `OTEL_SERVICE_NAME` and include `service.instance.id` in `OTEL_RESOURCE_ATTRIBUTES` before startup. The example uses `resource.Default()` to share these values across log fields, trace and metric resources, and profile tags. The Pyroscope tracer provider wrapper adds local root trace and span IDs to CPU samples and adds `pyroscope.profile.id` to the corresponding span. Request logs and metric exemplars use the request context.

An application's authenticated administration interface can call each control's `Update` method, check its returned error, and read `Status` for the resulting state. The package does not provide an administration interface. Runtime updates do not reread environment variables. Inner diagnostic handlers must permit DEBUG records when runtime settings permit DEBUG. Independent sink thresholds still apply. Required audit output must use handlers outside the diagnostic wrapper.

## Build metadata

Consumers must provide valid gklog build metadata through their normal build pipeline. Builds that do not provide valid metadata should fail before application startup.

## Releasing

After merging changes, tag a new version (or let consumers pin a pseudo version with `go get goodkind.io/gklog@<commit>`). Downstream modules update their `require` and drop any temporary `replace` directives pointing at a local checkout.
