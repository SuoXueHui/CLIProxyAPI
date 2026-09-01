//go:build !unix

package lifecycle

import "fmt"

type WriterLease struct{}

func AcquireWriterLease(string) (*WriterLease, error) {
	return nil, fmt.Errorf("writer lease is unsupported on this platform")
}

func (l *WriterLease) Release() error { return nil }
