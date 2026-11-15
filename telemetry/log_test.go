package telemetry_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/gklog"
	"goodkind.io/gklog/telemetry"
)

func TestLogControlDerivativesPreserveSinkFiltersAndAudit(t *testing.T) {
	t.Parallel()
	control, err := telemetry.NewLogControl(telemetry.LogSettings{
		Enabled: true,
		Level:   slog.LevelInfo,
	})
	if err != nil {
		t.Fatal(err)
	}

	var diagnostics, filtered, audit bytes.Buffer
	logger := slog.New(
		gklog.NewTeeHandler(
			control.Handler(
				slog.NewJSONHandler(&diagnostics, &slog.HandlerOptions{Level: slog.LevelDebug}),
			),
			control.Handler(
				slog.NewJSONHandler(&filtered, &slog.HandlerOptions{Level: slog.LevelError}),
			),
			slog.NewJSONHandler(&audit, &slog.HandlerOptions{Level: slog.LevelDebug}),
		),
	)
	derived := logger.With("request", "r1").WithGroup("job")
	derived.Debug("initial debug")
	derived.Info("initial info", "id", "j1")
	if err := control.Update(telemetry.LogSettings{
		Enabled: true,
		Level:   slog.LevelDebug,
	}); err != nil {
		t.Fatal(err)
	}

	derived.Debug("updated debug")
	if err := control.Update(telemetry.LogSettings{
		Enabled: false,
		Level:   slog.LevelDebug,
	}); err != nil {
		t.Fatal(err)
	}

	derived.Error("disabled error")
	if err := control.Update(telemetry.LogSettings{
		Enabled: true,
		Level:   slog.LevelWarn,
	}); err != nil {
		t.Fatal(err)
	}

	logger.Info("raised level")
	derived.Error("enabled error")
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}

	derived.Error("closed error")
	if err := control.Update(telemetry.LogSettings{Enabled: true}); !errors.Is(
		err,
		telemetry.ErrClosed,
	) {
		t.Fatalf("update after close = %v", err)
	}

	if status := control.Status(); !status.Closed || status.Enabled {
		t.Fatalf("closed status = %+v", status)
	}

	for _, message := range []string{"initial info", "updated debug", "enabled error"} {
		if !strings.Contains(diagnostics.String(), message) {
			t.Errorf("diagnostics omitted %q: %s", message, diagnostics.String())
		}
	}

	for _, message := range []string{
		"initial debug",
		"disabled error",
		"raised level",
		"closed error",
	} {
		if strings.Contains(diagnostics.String(), message) {
			t.Errorf("diagnostics included %q: %s", message, diagnostics.String())
		}
	}

	if !strings.Contains(diagnostics.String(), `"request":"r1"`) ||
		!strings.Contains(diagnostics.String(), `"job":{"id":"j1"}`) {
		t.Fatalf("derived attributes are missing: %s", diagnostics.String())
	}

	if strings.Count(filtered.String(), "\n") != 1 ||
		!strings.Contains(filtered.String(), "enabled error") {
		t.Fatalf("ERROR sink output = %s", filtered.String())
	}

	if strings.Count(audit.String(), "\n") != 7 ||
		!strings.Contains(audit.String(), "closed error") {
		t.Fatalf("independent audit output = %s", audit.String())
	}
}

func TestLogControlForwardsHandlerCleanupAndErrors(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "diagnostics.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = file.Close() })
	control, err := telemetry.NewLogControl(telemetry.LogSettings{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	handler := control.Handler(&fileHandler{
		Handler: slog.NewJSONHandler(file, nil),
		file:    file,
	})
	derived := handler.WithAttrs([]slog.Attr{slog.String("request", "r1")}).WithGroup("job")
	if !derived.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("initial control did not enable the pending record")
	}

	if err := control.Close(); err != nil {
		t.Fatal(err)
	}

	if err := derived.Handle(
		context.Background(),
		slog.NewRecord(time.Now(), slog.LevelInfo, "pending record", 0),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := file.WriteString("still open\n"); err != nil {
		t.Fatalf("control closed the child file: %v", err)
	}

	closer, ok := derived.(io.Closer)
	if !ok {
		t.Fatal("derived handler does not implement io.Closer")
	}

	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := file.WriteString("closed\n"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("file write after handler cleanup = %v", err)
	}

	contents, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}

	if string(contents) != "still open\n" {
		t.Fatalf("closed control wrote a pending record: %s", contents)
	}

	active, err := telemetry.NewLogControl(telemetry.LogSettings{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	err = active.Handler(slog.NewJSONHandler(file, nil)).Handle(
		context.Background(),
		slog.NewRecord(time.Now(), slog.LevelInfo, "closed file", 0),
	)
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("handler did not preserve the file error: %v", err)
	}
}

type fileHandler struct {
	slog.Handler
	file *os.File
}

func (h *fileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fileHandler{Handler: h.Handler.WithAttrs(attrs), file: h.file}
}

func (h *fileHandler) WithGroup(name string) slog.Handler {
	return &fileHandler{Handler: h.Handler.WithGroup(name), file: h.file}
}

func (h *fileHandler) Close() error { return h.file.Close() }

func TestLogValueCanUpdateControl(t *testing.T) {
	t.Parallel()
	control, err := telemetry.NewLogControl(telemetry.LogSettings{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	logger := slog.New(control.Handler(slog.NewJSONHandler(&output, nil)))
	logger.Info("change settings", "settings", controlLogValue{control: control})
	logger.Info("suppressed")
	if !strings.Contains(output.String(), "updated") ||
		strings.Contains(output.String(), "suppressed") {
		t.Fatalf("log value update did not apply: %s", output.String())
	}
}

type controlLogValue struct {
	control *telemetry.LogControl
}

func (v controlLogValue) LogValue() slog.Value {
	if err := v.control.Update(telemetry.LogSettings{
		Enabled: true,
		Level:   slog.LevelError,
	}); err != nil {
		return slog.StringValue(err.Error())
	}

	return slog.StringValue("updated")
}

func TestLogControlConcurrentUpdatesStatusAndRecords(t *testing.T) {
	t.Parallel()
	control, err := telemetry.NewLogControl(telemetry.LogSettings{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(control.Handler(slog.NewJSONHandler(io.Discard, nil))).WithGroup("job")
	var workers sync.WaitGroup

	for range 4 {
		workers.Go(func() {
			for index := range 100 {
				if err := control.Update(telemetry.LogSettings{
					Enabled: index%2 == 0,
				}); err != nil &&
					!errors.Is(err, telemetry.ErrClosed) {
					t.Error(err)
				}

				logger.Info("concurrent record", "index", index)
				_ = control.Status()
			}
		})
	}

	workers.Go(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	workers.Wait()
	if logger.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("closed control still enables diagnostic records")
	}
}

func TestLogControlRejectsInvalidLevels(t *testing.T) {
	t.Parallel()

	for _, level := range []slog.Level{slog.LevelDebug - 1, slog.LevelInfo + 1, slog.LevelError + 4} {
		if _, err := telemetry.NewLogControl(telemetry.LogSettings{Level: level}); err == nil {
			t.Errorf("constructor accepted level %s", level)
		}
	}

	control, err := telemetry.NewLogControl(telemetry.LogSettings{
		Enabled: true,
		Level:   slog.LevelWarn,
	})
	if err != nil {
		t.Fatal(err)
	}

	before := control.Status()
	if err := control.Update(telemetry.LogSettings{Level: 1}); err == nil {
		t.Fatal("update accepted an invalid level")
	}

	if after := control.Status(); after != before {
		t.Fatalf("invalid update changed status: %+v", after)
	}
}
