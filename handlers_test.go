package gklog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTeeHandler_HandleErrorPropagates(t *testing.T) {
	t.Parallel()
	th := NewTeeHandler(errHandler{}, slog.NewJSONHandler(io.Discard, nil))
	err := th.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0))
	if err == nil || !strings.Contains(err.Error(), "handler error") {
		t.Fatalf("want handler error, got %v", err)
	}
}

func TestTeeHandler_Enabled(t *testing.T) {
	t.Parallel()
	disabled := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})
	th := NewTeeHandler(disabled, slog.NewJSONHandler(io.Discard, nil))
	if th.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("expected disabled at debug when both handlers reject")
	}
	th2 := NewTeeHandler(slog.NewJSONHandler(io.Discard, nil), disabled)
	if !th2.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected enabled when one child accepts")
	}
}

func TestTeeHandler_WithAttrs(t *testing.T) {
	t.Parallel()
	base := slog.NewJSONHandler(io.Discard, nil)
	th := NewTeeHandler(base).WithAttrs([]slog.Attr{slog.String("a", "b")})
	if th == nil {
		t.Fatal("nil handler")
	}
}

func TestTeeHandler_WithGroup(t *testing.T) {
	t.Parallel()
	base := slog.NewJSONHandler(io.Discard, nil)
	th := NewTeeHandler(base).WithGroup("g")
	if th == nil {
		t.Fatal("nil handler")
	}
}

func TestTextHandler_WithGroup(t *testing.T) {
	t.Parallel()
	th := NewTextHandler(io.Discard, "").WithGroup("g")
	if th == nil {
		t.Fatal("nil handler")
	}
}

func TestTextHandler_Handle(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	th := NewTextHandler(&b, "")
	rec := slog.NewRecord(time.Now(), slog.LevelWarn, "hello", 0)
	rec.Add("x", 1)
	if err := th.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "hello") || !strings.Contains(b.String(), "x=1") {
		t.Fatalf("output=%q", b.String())
	}
}

func TestNewLockedWriteCloserSerializesChunkedWritersAcrossInstances(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shared.jsonl")

	w1, err := newChunkedAppendWriter(logPath)
	if err != nil {
		t.Fatalf("newChunkedAppendWriter #1: %v", err)
	}
	defer func() { _ = w1.Close() }()
	w2, err := newChunkedAppendWriter(logPath)
	if err != nil {
		t.Fatalf("newChunkedAppendWriter #2: %v", err)
	}
	defer func() { _ = w2.Close() }()

	l1 := NewLockedWriteCloser(logPath, w1)
	l2 := NewLockedWriteCloser(logPath, w2)
	defer func() { _ = l1.Close() }()
	defer func() { _ = l2.Close() }()

	linesPerWriter := 50
	var wg sync.WaitGroup
	writeLines := func(prefix string, w io.Writer) {
		defer wg.Done()
		for i := 0; i < linesPerWriter; i++ {
			line := fmt.Sprintf("{\"writer\":%q,\"seq\":%d}\n", prefix, i)
			if _, err := w.Write([]byte(line)); err != nil {
				t.Errorf("write %s/%d: %v", prefix, i, err)
				return
			}
		}
	}

	wg.Add(2)
	go writeLines("one", l1)
	go writeLines("two", l2)
	wg.Wait()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if got, want := len(lines), linesPerWriter*2; got != want {
		t.Fatalf("line count = %d want %d\n%s", got, want, string(data))
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "{\"writer\":") || !strings.HasSuffix(line, "}") {
			t.Fatalf("line %d malformed: %q", i, line)
		}
	}
}

type chunkedAppendWriter struct {
	f *os.File
}

func newChunkedAppendWriter(path string) (*chunkedAppendWriter, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &chunkedAppendWriter{f: f}, nil
}

func (w *chunkedAppendWriter) Write(p []byte) (int, error) {
	mid := len(p) / 2
	if mid == 0 {
		mid = len(p)
	}
	n1, err := w.f.Write(p[:mid])
	if err != nil {
		return n1, err
	}
	time.Sleep(2 * time.Millisecond)
	n2, err := w.f.Write(p[mid:])
	return n1 + n2, err
}

func (w *chunkedAppendWriter) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	return w.f.Close()
}

type errHandler struct{}

func (errHandler) Enabled(context.Context, slog.Level) bool { return true }

func (errHandler) Handle(context.Context, slog.Record) error {
	return errors.New("handler error")
}

func (errHandler) WithAttrs([]slog.Attr) slog.Handler { return errHandler{} }

func (errHandler) WithGroup(string) slog.Handler { return errHandler{} }
