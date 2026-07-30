package executor

import (
	"strings"
	"testing"
)

func TestLoginProbeEnvironmentExcludesCredentialBearingNetworkSettings(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/tmp/login-probe-home")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("HTTPS_PROXY", "https://user:secret@example.invalid")
	t.Setenv("ALL_PROXY", "socks5://user:secret@example.invalid")
	t.Setenv("SSL_CERT_FILE", "/private/daemon/ca.pem")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/private/daemon/node-ca.pem")
	t.Setenv("GITHUB_TOKEN", "daemon-secret")

	env := captureEnvironment().loginProbeEnvironment()
	joined := strings.Join(env, "\n")
	for _, name := range []string{
		"HTTPS_PROXY=",
		"ALL_PROXY=",
		"SSL_CERT_FILE=",
		"NODE_EXTRA_CA_CERTS=",
		"GITHUB_TOKEN=",
	} {
		if strings.Contains(joined, name) {
			t.Errorf("login-shell probe received %s", name)
		}
	}
	for _, name := range []string{"PATH=", "HOME=", "LANG="} {
		if !strings.Contains(joined, name) {
			t.Errorf("login-shell probe lost required %s", name)
		}
	}
}
