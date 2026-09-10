package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

// Registering a daemon found over mDNS pins the identity observed at discovery
// time. Between the browse that proposed a peer and the click that registers
// it, anything on the LAN could have taken over the name — this is what stops
// that becoming a registry entry.
func TestRegisterInstanceHonoursExpectInstanceID(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)

	tests := []struct {
		name     string
		expectID string
		wantCode int
	}{
		{"matching id registers", "srv-a", http.StatusCreated},
		{"absent pin keeps today's behaviour", "", http.StatusCreated},
		{"mismatched id is refused", "srv-b", http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHub(t, nil, config.RoutingConfig{})
			body := fmt.Sprintf(`{"base_url":%q,"token":"t"`, remote.URL)
			if tt.expectID != "" {
				body += fmt.Sprintf(`,"expect_instance_id":%q`, tt.expectID)
			}
			body += "}"

			rec := f.do(t, http.MethodPost, "/instances", body)
			if rec.Code != tt.wantCode {
				t.Fatalf("POST /instances = %d (%s), want %d",
					rec.Code, strings.TrimSpace(rec.Body.String()), tt.wantCode)
			}
			if tt.wantCode != http.StatusConflict {
				return
			}
			// The refusal has to say what it actually found, or the operator
			// cannot tell a stale proposal from an impostor.
			if msg := rec.Body.String(); !strings.Contains(msg, "srv-a") || !strings.Contains(msg, "srv-b") {
				t.Errorf("error should name both the observed and expected id, got %s", msg)
			}
		})
	}
}

// skip_probe means nothing contacts the instance, so there is no identity to
// check the pin against. Silently ignoring it would be worse than refusing:
// the caller would believe it had a guarantee it never got.
func TestRegisterInstanceRejectsAnUnverifiablePin(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	rec := f.do(t, http.MethodPost, "/instances",
		`{"base_url":"http://srv-a.local:7842","token":"t","skip_probe":true,"expect_instance_id":"srv-a"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /instances = %d (%s), want 400",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}

// The attack the address_changed banner opened, and the reason PATCH now
// verifies the way POST does.
//
// The banner is raised from an unauthenticated /health, which anything on the
// link can forge. A rogue daemon that advertises over mDNS and answers with a
// registered instance's id is classified as that instance having moved, and
// the GUI renders a one-click repair of an urgent-looking failure. If the
// click were accepted unverified, the hub would then send that instance's API
// token, its dispatched work, and every config push to the attacker.
func TestRepointingAnInstanceRequiresItToProveItsIdentity(t *testing.T) {
	// An impostor: claims the right id on /health, but does not hold the
	// instance's token, so every authenticated call fails.
	impostor := newFakeInstance(t, "srv-a", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok","instance_id":"srv-a",` +
				`"instance_name":"srv-a","role":"worker"}`))
			return
		}
		// It has no idea what the real token is.
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	})

	real := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": real}, config.RoutingConfig{})

	rec := f.do(t, http.MethodPatch, "/instances/srv-a",
		fmt.Sprintf(`{"base_url":%q}`, impostor.URL))

	if rec.Code == http.StatusOK {
		t.Fatal("the hub re-pointed a registered instance at a host that only " +
			"claimed its id; the token would now go to the attacker")
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("PATCH = %d (%s), want 502",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if body := rec.Body.String(); !strings.Contains(body, "token") {
		t.Errorf("the refusal should say the token was not accepted, got %s", body)
	}
}

// A host that does not even claim the right id is refused earlier, and the
// message says which id it found — an operator has to be able to tell a stale
// proposal from an impostor.
func TestRepointingRefusesADifferentDaemon(t *testing.T) {
	other := newFakeInstance(t, "srv-b", nil)
	real := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": real}, config.RoutingConfig{})

	rec := f.do(t, http.MethodPatch, "/instances/srv-a",
		fmt.Sprintf(`{"base_url":%q}`, other.URL))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("PATCH = %d (%s), want 502", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if body := rec.Body.String(); !strings.Contains(body, "srv-b") {
		t.Errorf("the refusal should name the id it actually found, got %s", body)
	}
}

// Changing anything other than the address does not probe: renaming an
// instance that happens to be down must keep working.
func TestPatchingSomethingElseDoesNotProbe(t *testing.T) {
	real := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": real}, config.RoutingConfig{})

	if rec := f.do(t, http.MethodPatch, "/instances/srv-a", `{"name":"Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename = %d (%s), want 200", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}

// And an operator moving a machine that is currently off can still say so.
func TestRepointingCanBeForced(t *testing.T) {
	real := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": real}, config.RoutingConfig{})

	rec := f.do(t, http.MethodPatch, "/instances/srv-a",
		`{"base_url":"http://srv-a-moved.local:7842","skip_probe":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("forced re-point = %d (%s), want 200",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}
