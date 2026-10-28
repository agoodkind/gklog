package gklog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"goodkind.io/gklog"
)

const rotationPayloadSize = 600 * 1024

func TestFileJSONAppendsAfterRotatedFileTrim(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	compress := false
	handler := gklog.FileJSON(path, slog.LevelInfo, gklog.RotationConfig{MaxSizeMB: 1, Compress: &compress})
	closer, ok := handler.(io.Closer)
	if !ok {
		t.Fatal("FileJSON handler does not implement io.Closer")
	}
	t.Cleanup(func() { closeRotationWriter(t, closer) })
	payload := strings.Repeat("x", rotationPayloadSize)
	writeRotationRecord(t, handler, "before rotation", payload)
	writeRotationRecord(t, handler, "after rotation", payload)
	archives, err := filepath.Glob(filepath.Join(filepath.Dir(path), "events-*.jsonl"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("rotation archives = %v, error = %v", archives, err)
	}

	mutateRotationFile(t, path, func() error {
		return os.WriteFile(path, []byte("{\"msg\":\"retained\"}\n"), 0o600)
	})
	writeRotationRecord(t, handler, "after trim", "")
	writeRotationRecord(t, handler, "continued append", "")
	assertRotationMessages(t, path, "retained", "after trim", "continued append")
	writeRotationRecord(t, handler, "before next rotation", payload)
	writeRotationRecord(t, handler, "next rotation", payload)
	assertRotationMessages(t, path, "next rotation")
}

func TestLockedLumberjackWritersAppendAndFollowOtherWriterRotation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	first := newRotationWriter(t, path)
	second := newRotationWriter(t, path)
	writeRotationJSON(t, first, "first", "")
	writeRotationJSON(t, second, "second", "")
	writeRotationJSON(t, first, "third", "")
	assertRotationMessages(t, path, "first", "second", "third")

	payload := strings.Repeat("x", rotationPayloadSize)
	writeRotationJSON(t, first, "before rotation", payload)
	writeRotationJSON(t, second, "after rotation", payload)
	writeRotationJSON(t, first, "follow rotation", "")
	writeRotationJSON(t, second, "continued append", "")
	assertRotationMessages(t, path, "after rotation", "follow rotation", "continued append")
	archives, err := filepath.Glob(filepath.Join(filepath.Dir(path), "events-*.jsonl"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("rotation archives = %v, error = %v", archives, err)
	}
	assertRotationMessages(t, archives[0], "first", "second", "third", "before rotation")
}

func TestLockedLumberjackWriterKeepsFileAtRotationLimit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer := newRotationWriter(t, path)
	const recordSize = 512 * 1024
	const jsonOverhead = len("{\"msg\":\"first\",\"payload\":\"\"}\n")
	payload := strings.Repeat("x", recordSize-jsonOverhead)
	writeRotationJSON(t, writer, "first", payload)
	writeRotationJSON(t, writer, "other", payload)
	assertRotationMessages(t, path, "first", "other")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 2*recordSize {
		t.Fatalf("file size = %d, want %d", info.Size(), 2*recordSize)
	}
}

func TestLockedLumberjackWriterFollowsReplacementWithSameSizeAndTime(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer := newRotationWriter(t, path)
	writeRotationJSON(t, writer, "original", "")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := path + ".old"
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mutateRotationFile(t, path, func() error {
		if renameErr := os.Rename(path, archive); renameErr != nil {
			return renameErr
		}
		if writeErr := os.WriteFile(path, bytes.ReplaceAll(original, []byte("original"), []byte("replaced")), 0o600); writeErr != nil {
			return writeErr
		}
		return os.Chtimes(path, info.ModTime(), info.ModTime())
	})
	writeRotationJSON(t, writer, "after replacement", "")
	assertRotationMessages(t, path, "replaced", "after replacement")
	assertRotationMessages(t, archive, "original")
}

func TestLockedLumberjackWriterRecreatesRemovedPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer := newRotationWriter(t, path)
	writeRotationJSON(t, writer, "original", "")
	mutateRotationFile(t, path, func() error { return os.Remove(path) })
	writeRotationJSON(t, writer, "after removal", "")
	assertRotationMessages(t, path, "after removal")
}

func newRotationWriter(t *testing.T, path string) io.WriteCloser {
	t.Helper()
	compress := false
	writer := gklog.NewLockedWriteCloser(path, gklog.NewLumberjackWriterWithConfig(path, gklog.RotationConfig{
		MaxSizeMB: 1,
		Compress:  &compress,
	}))
	t.Cleanup(func() { closeRotationWriter(t, writer) })
	return writer
}

func closeRotationWriter(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Error(err)
	}
}

func mutateRotationFile(t *testing.T, path string, mutate func() error) {
	t.Helper()
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			t.Error(err)
		}
	}()
	if err := mutate(); err != nil {
		t.Fatal(err)
	}
}

func writeRotationRecord(t *testing.T, handler slog.Handler, message, payload string) {
	t.Helper()
	record := slog.NewRecord(time.Now(), slog.LevelInfo, message, 0)
	record.AddAttrs(slog.String("payload", payload))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func writeRotationJSON(t *testing.T, writer io.Writer, message, payload string) {
	t.Helper()
	data, err := json.Marshal(struct {
		Message string `json:"msg"`
		Payload string `json:"payload"`
	}{Message: message, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if count, writeErr := writer.Write(data); writeErr != nil || count != len(data) {
		t.Fatalf("write count = %d, want %d; error = %v", count, len(data), writeErr)
	}
}

func assertRotationMessages(t *testing.T, path string, expected ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		t.Fatal("log contains NUL bytes after file mutation")
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("log does not end with a newline")
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) != len(expected) {
		t.Fatalf("record count = %d, want %d", len(lines), len(expected))
	}
	for index, line := range lines {
		var record struct {
			Message string `json:"msg"`
		}
		if decodeErr := json.Unmarshal(line, &record); decodeErr != nil {
			t.Fatalf("record %d is invalid JSON: %v", index, decodeErr)
		}
		if record.Message != expected[index] {
			t.Errorf("record %d message = %q, want %q", index, record.Message, expected[index])
		}
	}
}
