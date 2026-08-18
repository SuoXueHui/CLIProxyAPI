//go:build linux

package filetime

import (
	"time"

	"golang.org/x/sys/unix"
)

func platformCreationTime(path string) (time.Time, bool) {
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME, &stat); err != nil {
		return time.Time{}, false
	}
	if stat.Mask&unix.STATX_BTIME == 0 || stat.Btime.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)), true
}
