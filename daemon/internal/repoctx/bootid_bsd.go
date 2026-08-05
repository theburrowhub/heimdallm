//go:build darwin || freebsd || netbsd || openbsd

package repoctx

import (
	"encoding/hex"
	"syscall"
)

// readBootIdentity reads kern.boottime, whose payload is a struct timeval. The
// bytes are hex-encoded and used opaquely rather than decoded: the prune path
// only ever compares this token for equality, and hex-encoding sidesteps both
// the 32/64-bit timeval layout difference and the embedded NULs that make the
// raw string awkward to store in JSON.
func readBootIdentity() (string, error) {
	raw, err := syscall.Sysctl("kern.boottime")
	if err != nil {
		return "", err
	}
	if raw == "" {
		return "", syscall.EINVAL
	}
	return "boottime-" + hex.EncodeToString([]byte(raw)), nil
}
