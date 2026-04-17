package gklog

import (
	"testing"
)

func TestNewReturnsLogger(t *testing.T) {
	t.Parallel()
	log, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if log == nil {
		t.Fatal("nil logger")
	}
}
