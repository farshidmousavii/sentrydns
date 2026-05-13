package store

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

func TestStore(t *testing.T) {
	f, _ := os.CreateTemp("", "store-test-*.txt")
	f.Close()
	defer os.Remove(f.Name())

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(f.Name(), discardLog)
	if err != nil {
		t.Fatal(err)
	}

	if s.IsIran("digikala.com") {
		t.Error("expected false, got true")
	}

	s.Add("digikala.com")

	if !s.IsIran("digikala.com") {
		t.Error("expected true, got false")
	}

	if !s.IsIran("www.digikala.com") {
		t.Error("expected true for subdomain, got false")
	}
}
