// Package filetime exposes a stable file creation time when the filesystem
// provides one, with callers responsible for choosing a fallback.
package filetime

import "time"

// CreationTime returns the filesystem birth/creation time for path. A zero
// value means the filesystem or platform did not expose that metadata.
func CreationTime(path string) time.Time {
	if created, ok := platformCreationTime(path); ok && !created.IsZero() {
		return created
	}
	return time.Time{}
}
