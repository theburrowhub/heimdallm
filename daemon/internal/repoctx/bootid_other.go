//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package repoctx

import "errors"

// readBootIdentity has no portable implementation here. Returning an error keeps
// bootIdentity() empty, which disables the boot-change valve and leaves the
// original fail-closed retention in place — the safe direction.
func readBootIdentity() (string, error) {
	return "", errors.New("repoctx: boot identity unavailable on this platform")
}
