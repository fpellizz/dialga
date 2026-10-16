//go:build !linux && !darwin && !windows

package transfer

import "errors"

// freeBytes is not known on this OS: the download skips the check.
func freeBytes(string) (uint64, error) {
	return 0, errors.ErrUnsupported
}
