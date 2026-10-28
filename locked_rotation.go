package gklog

import (
	"os"

	"gopkg.in/natefinch/lumberjack.v2"
)

// lockedRotation tracks the active file while lockedWriteCloser owns the
// path lock. Lumberjack caches the file size and may open without O_APPEND.
type lockedRotation struct {
	path     string
	logger   *lumberjack.Logger
	lastFile os.FileInfo
}

func (r *lockedRotation) refresh() error {
	info, err := os.Stat(r.path)
	if err != nil && !os.IsNotExist(err) {
		return &lockedWriteError{op: "stat before write", err: err}
	}
	if r.lastFile != nil && info != nil &&
		os.SameFile(r.lastFile, info) &&
		r.lastFile.Size() == info.Size() &&
		r.lastFile.ModTime().Equal(info.ModTime()) {
		return nil
	}
	// Close discards lumberjack's cached descriptor. Its next Write opens
	// the current path and reads its size before appending or rotating.
	if err := r.logger.Close(); err != nil {
		return &lockedWriteError{op: "reopen", err: err}
	}
	r.lastFile = nil
	return nil
}

func (r *lockedRotation) remember() error {
	info, err := os.Stat(r.path)
	if err != nil {
		return &lockedWriteError{op: "stat after write", err: err}
	}
	r.lastFile = info
	return nil
}
