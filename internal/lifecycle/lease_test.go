//go:build unix

package lifecycle

import (
	"testing"
)

func TestWriterLeaseIsExclusive(t *testing.T) {
	path := t.TempDir() + "/writer.lock"
	first, errFirst := AcquireWriterLease(path)
	if errFirst != nil {
		t.Fatalf("AcquireWriterLease(first) error = %v", errFirst)
	}
	defer func() { _ = first.Release() }()
	if _, errSecond := AcquireWriterLease(path); errSecond == nil {
		t.Fatal("second writer lease unexpectedly succeeded")
	}
	if errRelease := first.Release(); errRelease != nil {
		t.Fatalf("Release() error = %v", errRelease)
	}
	second, errSecond := AcquireWriterLease(path)
	if errSecond != nil {
		t.Fatalf("AcquireWriterLease(after release) error = %v", errSecond)
	}
	_ = second.Release()
}
