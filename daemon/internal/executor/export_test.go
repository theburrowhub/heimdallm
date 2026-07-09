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
