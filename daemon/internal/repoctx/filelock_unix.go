//go:build darwin || linux

package repoctx

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryExclusiveFileLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EAGAIN):
		return false, nil
	default:
		return false, err
	}
}
