package executor

// SetLoginShellLookPathForTest overrides the login-shell CLI probe so tests can
// stay hermetic. The real probe sources the developer's shell profile and would
// otherwise resolve a CLI installed outside the test's $PATH, defeating $PATH
// isolation. Returns a restore func to defer.
func SetLoginShellLookPathForTest(f func(string) string) func() {
	old := loginShellLookPath
	loginShellLookPath = f
	return func() { loginShellLookPath = old }
}

// SetWellKnownBinDirsForTest overrides the well-known installer directory
// probe so tests can stay hermetic. Returns a restore func to defer.
func SetWellKnownBinDirsForTest(f func() []string) func() {
	old := wellKnownBinDirs
	wellKnownBinDirs = f
	return func() { wellKnownBinDirs = old }
}

// DefaultWellKnownBinDirsForTest exposes the production directory derivation.
func DefaultWellKnownBinDirsForTest() []string {
	return wellKnownBinDirs()
}
