package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// dartConfigPath is the Flutter Settings screen, which carries a client-side
// copy of the poll_interval bounds for immediate-feedback validation. The
// daemon is authoritative (minPollInterval/maxPollInterval, enforced by
// ValidatePollInterval); this guard makes sure the GUI's copy can't silently
// drift from it. Path is relative to this package directory (daemon/internal/
// config), so ../../../ lands at the repo root. See issue #546.
const dartConfigPath = "../../../flutter_app/lib/features/config/config_screen.dart"

var (
	dartMinPollRe = regexp.MustCompile(`_minPollInterval\s*=\s*Duration\(([^)]*)\)`)
	dartMaxPollRe = regexp.MustCompile(`_maxPollInterval\s*=\s*Duration\(([^)]*)\)`)
	// Named arguments of Dart's Duration() constructor, e.g. `minutes: 1`.
	dartDurationArgRe = regexp.MustCompile(`(days|hours|minutes|seconds|milliseconds|microseconds)\s*:\s*([0-9]+)`)
)

// TestPollIntervalBounds_MatchFlutterGUI fails if the [1m, 24h] poll_interval
// range encoded in the Flutter GUI diverges from the authoritative daemon
// bounds. It catches the silent drift described in issue #546: a change to the
// Go constants without a matching change to the Dart guard (or vice versa).
func TestPollIntervalBounds_MatchFlutterGUI(t *testing.T) {
	src, err := os.ReadFile(dartConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			// The GUI lives in the same repo, so in CI this file is always
			// present. Skip (rather than fail) for daemon-only checkouts where
			// there is no Dart copy that could drift.
			t.Skipf("Flutter config screen not present at %s; skipping cross-language drift check", filepath.Clean(dartConfigPath))
		}
		t.Fatalf("read %s: %v", dartConfigPath, err)
	}

	gotMin := parseDartDuration(t, "_minPollInterval", dartMinPollRe, src)
	gotMax := parseDartDuration(t, "_maxPollInterval", dartMaxPollRe, src)

	if gotMin != minPollInterval {
		t.Errorf("poll_interval lower bound drift: daemon minPollInterval=%s but Flutter _minPollInterval=%s\n"+
			"update %s to match daemon/internal/config/config.go (see issue #546)", minPollInterval, gotMin, dartConfigPath)
	}
	if gotMax != maxPollInterval {
		t.Errorf("poll_interval upper bound drift: daemon maxPollInterval=%s but Flutter _maxPollInterval=%s\n"+
			"update %s to match daemon/internal/config/config.go (see issue #546)", maxPollInterval, gotMax, dartConfigPath)
	}
}

// parseDartDuration extracts the Dart Duration(...) literal named by re and
// converts it to a time.Duration, summing all named arguments so equivalent
// spellings (e.g. Duration(hours: 24) vs Duration(days: 1)) compare equal.
func parseDartDuration(t *testing.T, name string, re *regexp.Regexp, src []byte) time.Duration {
	t.Helper()
	m := re.FindSubmatch(src)
	if m == nil {
		t.Fatalf("could not find %s = Duration(...) in %s; the drift guard needs updating (issue #546)", name, dartConfigPath)
	}
	args := dartDurationArgRe.FindAllSubmatch(m[1], -1)
	if len(args) == 0 {
		t.Fatalf("could not parse Duration arguments for %s (got %q)", name, m[1])
	}
	var total time.Duration
	for _, a := range args {
		n, err := strconv.Atoi(string(a[2]))
		if err != nil {
			t.Fatalf("invalid numeric literal %q for %s: %v", a[2], name, err)
		}
		total += dartDurationUnit(string(a[1])) * time.Duration(n)
	}
	return total
}

// dartDurationUnit maps a Dart Duration named argument to its time.Duration
// equivalent.
func dartDurationUnit(unit string) time.Duration {
	switch unit {
	case "days":
		return 24 * time.Hour
	case "hours":
		return time.Hour
	case "minutes":
		return time.Minute
	case "seconds":
		return time.Second
	case "milliseconds":
		return time.Millisecond
	case "microseconds":
		return time.Microsecond
	}
	return 0
}
