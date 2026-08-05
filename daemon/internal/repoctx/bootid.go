package repoctx

import "sync"

// bootIdentity returns an opaque token that changes when the machine reboots,
// or "" when this platform cannot report one.
//
// Only the identity matters, never the value's meaning: the prune path compares
// a leftover's stamp against the current one, so any stable-per-boot,
// changes-across-boots token works. Callers MUST treat "" as "unknown" and keep
// the fail-closed behaviour, never as "different".
//
// Resolved once. Re-reading per prune cycle would be wasted syscalls, and a
// value that changed mid-process would mean the machine rebooted underneath a
// running daemon, which is not a thing.
var bootIdentity = sync.OnceValue(func() string {
	id, err := readBootIdentity()
	if err != nil {
		return ""
	}
	return id
})

// bootLeftoverCollectable reports whether a leftover stamped with leaseBootID is
// provably dead because it predates the current boot.
//
// Both unknowns are treated as "not collectable": an empty stamp means the
// leftover was written before this field existed (or on a platform without boot
// identity), and an empty current identity means we cannot compare. In either
// case the caller keeps the leftover, which is exactly the behaviour that
// shipped before this valve existed.
func bootLeftoverCollectable(leaseBootID string) bool {
	current := bootIdentity()
	if current == "" || leaseBootID == "" {
		return false
	}
	return leaseBootID != current
}
