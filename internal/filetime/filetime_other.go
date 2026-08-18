//go:build !linux && !darwin

package filetime

import "time"

func platformCreationTime(string) (time.Time, bool) {
	return time.Time{}, false
}
