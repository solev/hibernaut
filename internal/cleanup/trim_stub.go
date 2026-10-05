//go:build !linux

package cleanup

import "errors"

// OpenTrimStorage reports that snapshot trimming needs Linux.
func OpenTrimStorage(string) (TrimStorage, error) {
	return nil, errors.New("snapshot trimming needs Linux")
}
