package workgate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	desktopRecoverySchemaVersion = 1
	maxRecoveryJournalBytes      = 64 << 10
)

type desktopRecoveryJournal struct {
	SchemaVersion          int     `json:"schemaVersion"`
	ExpectedVersion        string  `json:"expectedVersion"`
	Phase                  string  `json:"phase"`
	LeaseID                string  `json:"leaseID"`
	DaemonPID              *int32  `json:"daemonPID"`
	DaemonBootID           *string `json:"daemonBootID"`
	DaemonVersion          *string `json:"daemonVersion"`
	LaunchAgentWasLoaded   *bool   `json:"launchAgentWasLoaded"`
	LaunchAgentWasDisabled *bool   `json:"launchAgentWasDisabled"`
}

// RestoreDesktopRecoveryBarrier promotes a native updater recovery journal to
// a sealed Go workgate before daemon bootstrap. The daemon lifecycle lock must
// already be held by the caller, so no live daemon can admit work while an
// expired ordinary lease is upgraded.
//
// pendingInstall deliberately does not block startup: at that phase Sparkle
// has only downloaded an update and ordinary daemon work is still allowed.
// preparing and sealed prove the native updater durably crossed its idle-drain
// boundary but has not yet completed the process-bound release handshake. They
// remain fail-closed even when launchd respawns the daemon before the desktop
// app has relaunched to repair the marker itself.
//
// installing is deliberately not reconstructed when its marker is absent. The
// final DELETE may already have removed that marker immediately before a daemon
// crash and immediately before the app clears its journal. Recreating a seal
// in that acknowledged window could leave an ownerless barrier after the app
// legitimately clears the journal. If a marker still exists, the ordinary
// NewPersistent call that follows restores it without help from this function.
func RestoreDesktopRecoveryBarrier(
	leaseTTL time.Duration,
	statePath string,
	journalPath string,
) (bool, error) {
	leaseID, requiresBarrier, err := readDesktopRecoveryIntent(journalPath)
	if err != nil || !requiresBarrier {
		return requiresBarrier, err
	}

	gate, err := NewPersistent(leaseTTL, statePath)
	if err != nil {
		return true, fmt.Errorf("workgate: restore recovery barrier state: %w", err)
	}
	snapshot := gate.Status()
	if snapshot.Draining && snapshot.LeaseID != leaseID {
		return true, fmt.Errorf(
			"workgate: recovery journal lease conflicts with persistent barrier",
		)
	}
	if !snapshot.Draining {
		if _, err := gate.Prepare(leaseID); err != nil {
			return true, fmt.Errorf("workgate: prepare recovery barrier: %w", err)
		}
	}
	if !gate.Status().Sealed {
		if _, err := gate.Seal(leaseID); err != nil {
			return true, fmt.Errorf("workgate: seal recovery barrier: %w", err)
		}
	}
	return true, nil
}

func readDesktopRecoveryIntent(path string) (string, bool, error) {
	if strings.TrimSpace(path) == "" {
		return "", false, fmt.Errorf("workgate: desktop recovery journal path is empty")
	}
	data, err := readPrivateRegularFile(path, maxRecoveryJournalBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("workgate: read desktop recovery journal securely: %w", err)
	}
	var journal desktopRecoveryJournal
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return "", false, fmt.Errorf("workgate: decode desktop recovery journal: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return "", false, err
	}
	if journal.SchemaVersion != desktopRecoverySchemaVersion {
		return "", false, fmt.Errorf(
			"workgate: unsupported desktop recovery journal schema %d",
			journal.SchemaVersion,
		)
	}
	journal.LeaseID = strings.TrimSpace(journal.LeaseID)
	if !validRecoveryUUID(journal.LeaseID) {
		return "", false, fmt.Errorf("workgate: desktop recovery journal lease is not a UUID")
	}
	if strings.TrimSpace(journal.ExpectedVersion) == "" {
		return "", false, fmt.Errorf("workgate: desktop recovery journal version is empty")
	}

	switch journal.Phase {
	case "pendingInstall":
		if journal.DaemonPID != nil || journal.DaemonBootID != nil ||
			journal.DaemonVersion != nil || journal.LaunchAgentWasLoaded != nil ||
			journal.LaunchAgentWasDisabled != nil {
			return "", false, fmt.Errorf(
				"workgate: pending desktop recovery journal contains daemon identity",
			)
		}
		return journal.LeaseID, false, nil
	case "preparing", "sealed", "installing":
		if journal.DaemonPID == nil || *journal.DaemonPID <= 0 ||
			journal.DaemonBootID == nil || strings.TrimSpace(*journal.DaemonBootID) == "" ||
			journal.DaemonVersion == nil || strings.TrimSpace(*journal.DaemonVersion) == "" ||
			journal.LaunchAgentWasLoaded == nil || journal.LaunchAgentWasDisabled == nil {
			return "", false, fmt.Errorf(
				"workgate: active desktop recovery journal lacks daemon identity",
			)
		}
		return journal.LeaseID, journal.Phase != "installing", nil
	default:
		return "", false, fmt.Errorf(
			"workgate: unsupported desktop recovery phase %q",
			journal.Phase,
		)
	}
}

func readPrivateRegularFile(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("private file size limit must be positive")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("wrap private file descriptor")
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat private file: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("private file must be a current-user regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("private file must not be accessible by group or other users")
	}

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read private file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("private file exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("workgate: decode desktop recovery journal trailer: %w", err)
	}
	return fmt.Errorf("workgate: desktop recovery journal contains multiple values")
}

func validRecoveryUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !((char >= '0' && char <= '9') ||
				(char >= 'a' && char <= 'f') ||
				(char >= 'A' && char <= 'F')) {
				return false
			}
		}
	}
	return true
}
