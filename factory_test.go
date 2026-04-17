package gklog

import (
	"testing"
)

func TestNewReturnsLogger(t *testing.T) {
	t.Parallel()
	log, closer, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if log == nil {
		t.Fatal("nil logger")
	}
	if closer == nil {
		t.Fatal("nil closer")
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
}
