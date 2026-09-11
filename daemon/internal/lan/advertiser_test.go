package lan

import (
	"strings"
	"testing"
)

// A hostname the caller chose is held to the same rule a peer's is: publishing
// a name every hub refuses would make the daemon undiscoverable with the only
// evidence on someone else's machine.
func TestNewAdvertiserRejectsANonLocalHostname(t *testing.T) {
	for _, host := range []string{
		"hub.corp.example.com",
		"deep.sub.local",
		"-leading-hyphen.local",
		"has_underscore.local",
		strings.Repeat("x", 64) + ".local",
	} {
		conn, _ := NewMemConn()
		_, err := NewAdvertiser(conn, Advertisement{
			InstanceID: "id-1",
			Port:       7842,
			Hostname:   host,
		}, quietLogger())
		if err == nil {
			t.Fatalf("hostname %q: NewAdvertiser accepted it, want refusal", host)
		}
	}
}

// The rule must not reject the names we legitimately publish, including the
// trailing-dot form the advertiser itself normalises to.
func TestNewAdvertiserAcceptsLocalHostnames(t *testing.T) {
	for _, host := range []string{"srv-a.local", "srv-a.local.", "SRV-A.Local", "", "x.local"} {
		conn, _ := NewMemConn()
		a, err := NewAdvertiser(conn, Advertisement{
			InstanceID: "id-1",
			Port:       7842,
			Hostname:   host,
		}, quietLogger())
		if err != nil {
			t.Fatalf("hostname %q: NewAdvertiser refused it: %v", host, err)
		}
		if !strings.HasSuffix(a.ad.Hostname, ".local.") {
			t.Fatalf("hostname %q normalised to %q, want a .local. fqdn", host, a.ad.Hostname)
		}
	}
}
