//go:build darwin

package filetime

import (
	"syscall"
	"time"
)

func platformCreationTime(path string) (time.Time, bool) {
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil || stat.Birthtimespec.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec), true
}
