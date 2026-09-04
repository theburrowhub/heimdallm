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
