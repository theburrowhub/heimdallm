//go:build !darwin && !linux

package repoctx

import "os"

func tryExclusiveFileLock(_ *os.File) (bool, error) {
	return false, errFileLockUnsupported
}

func unlockFile(_ *os.File) error {
	return errFileLockUnsupported
}
