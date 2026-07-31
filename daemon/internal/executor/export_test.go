package executor

import "sync"

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

// AppendDirToPathForTest exposes appendDirToPath.
func AppendDirToPathForTest(env []string, dir string) []string {
	return appendDirToPath(env, dir)
}

// ResetLoginPathCacheForTest clears the process-wide login-shell PATH cache,
// which otherwise freezes os.Environ() at the first ExecuteRaw/cliHelp call
// and leaks one test's $PATH into every later subprocess. It resets
// immediately and returns a func to defer so the test also cleans up after
// itself, keeping tests order- and shuffle-independent.
func ResetLoginPathCacheForTest() func() {
	reset := func() {
		loginPathOnce = sync.Once{}
		loginPathEnv = nil
	}
	reset()
	return reset
}
