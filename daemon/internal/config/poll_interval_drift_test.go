package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// flutterAppDir is the root of the Flutter GUI module. Its presence is what
// distinguishes a daemon-only checkout (no Dart copy that could drift, so the
// guard skips) from an in-repo build where the GUI must exist (a missing config
// screen then means a rename/move that should fail the guard). Path is relative
// to this package directory (daemon/internal/config), so ../../../ lands at the
// repo root.
const flutterAppDir = "../../../flutter_app"

// dartConfigPath is the Flutter Settings screen, which carries a client-side
// copy of the poll_interval bounds for immediate-feedback validation. The
// daemon is authoritative (minPollInterval/maxPollInterval, enforced by
// ValidatePollInterval); this guard makes sure the GUI's copy can't silently
// drift from it. See issue #546.
const dartConfigPath = flutterAppDir + "/lib/features/config/config_screen.dart"

var (
	// The optional `const` tolerates an equivalent Dart refactor from
	// `= Duration(...)` to `= const Duration(...)`. `[^)]*` assumes the
	// argument list has no nested parentheses, which holds because Dart's
	// Duration only takes integer-literal named args (e.g. `minutes: 1`).
	// FindSubmatch validates only the first match of each constant, which is
	// sufficient: each is declared exactly once in the config screen.
	dartMinPollRe = regexp.MustCompile(`_minPollInterval\s*=\s*(?:const\s+)?Duration\(([^)]*)\)`)
	dartMaxPollRe = regexp.MustCompile(`_maxPollInterval\s*=\s*(?:const\s+)?Duration\(([^)]*)\)`)
	// Named arguments of Dart's Duration() constructor, e.g. `minutes: 1`.
	dartDurationArgRe = regexp.MustCompile(`(days|hours|minutes|seconds|milliseconds|microseconds)\s*:\s*([0-9]+)`)
)

// TestPollIntervalBounds_MatchFlutterGUI fails if the [1m, 24h] poll_interval
// range encoded in the Flutter GUI diverges from the authoritative daemon
// bounds (currently 1m and 24h). It catches the silent drift described in issue
// #546: a change to the Go constants without a matching change to the Dart
// guard (or vice versa).
func TestPollIntervalBounds_MatchFlutterGUI(t *testing.T) {
	src, err := os.ReadFile(dartConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Distinguish "no GUI here" from "GUI here but the file moved". A
			// daemon-only checkout has no flutter_app dir at all → skip, there
			// is nothing that could drift. But if flutter_app exists and only
			// the config screen is missing, the file was renamed/moved — which
			// is exactly the drift this guard must catch, so fail loudly rather
			// than silently turning into a no-op.
			if _, statErr := os.Stat(flutterAppDir); os.IsNotExist(statErr) {
				t.Skipf("Flutter app not present at %s; skipping cross-language drift check", filepath.Clean(flutterAppDir))
			}
			t.Fatalf("Flutter app exists but %s is missing — was it renamed/moved? Update this guard to match (issue #546)", dartConfigPath)
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
		unit, ok := dartDurationUnit(string(a[1]))
		if !ok {
			// Unreachable while dartDurationArgRe only captures known units;
			// the explicit failure makes the invariant safe if that regex is
			// ever broadened, rather than silently contributing zero.
			t.Fatalf("unrecognized Dart Duration unit %q for %s; update dartDurationUnit (issue #546)", a[1], name)
		}
		total += unit * time.Duration(n)
	}
	return total
}

// dartDurationUnit maps a Dart Duration named argument to its time.Duration
// equivalent. The bool is false for an unrecognized unit so callers can fail
// loudly instead of treating an unknown unit as a zero contribution.
func dartDurationUnit(unit string) (time.Duration, bool) {
	switch unit {
	case "days":
		return 24 * time.Hour, true
	case "hours":
		return time.Hour, true
	case "minutes":
		return time.Minute, true
	case "seconds":
		return time.Second, true
	case "milliseconds":
		return time.Millisecond, true
	case "microseconds":
		return time.Microsecond, true
	}
	return 0, false
}
