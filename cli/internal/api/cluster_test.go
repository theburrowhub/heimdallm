package api_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func newClusterServer(t *testing.T, handler http.HandlerFunc) (*api.Client, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(body))
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return api.New(srv.URL, "tok"), &seen
}

func TestListInstances(t *testing.T) {
	client, seen := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Heimdallm-Token"); got != "tok" {
			t.Errorf("token header = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"role":      "hub",
			"self_id":   "hub-1",
			"self_name": "Local hub",
			"instances": []map[string]any{
				{"id": "hub-1", "name": "Local hub", "self": true, "enabled": true},
				{
					"id":             "srv-a",
					"base_url":       "http://10.0.0.11:7842",
					"enabled":        true,
					"assigned_repos": 4,
					"state":          map[string]any{"reachable": true, "version": "0.9.0"},
				},
			},
		})
	})

	registry, err := client.ListInstances()
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if registry.SelfID != "hub-1" || len(registry.Instances) != 2 {
		t.Fatalf("registry = %+v", registry)
	}
	if registry.Instances[1].State.Version != "0.9.0" {
		t.Errorf("version = %q", registry.Instances[1].State.Version)
	}
	// Display name falls back to the id so a row never renders blank.
	if got := registry.Instances[1].DisplayName(); got != "srv-a" {
		t.Errorf("DisplayName() = %q, want the id", got)
	}
	if !strings.Contains((*seen)[0], "GET /instances") {
		t.Errorf("requests = %v", *seen)
	}
}

// A plain single-daemon install answers 404 on the control plane. That is the
// normal case, not a failure, so it gets its own sentinel.
func TestClusterCallsOnANonHub(t *testing.T) {
	client, _ := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"this daemon is not a cluster hub"}`))
	})

	if _, err := client.ListInstances(); !errors.Is(err, api.ErrNotAHub) {
		t.Errorf("ListInstances error = %v, want ErrNotAHub", err)
	}
	if _, err := client.GetRouting(); !errors.Is(err, api.ErrNotAHub) {
		t.Errorf("GetRouting error = %v, want ErrNotAHub", err)
	}
	if _, err := client.PropagateConfig(nil); !errors.Is(err, api.ErrNotAHub) {
		t.Errorf("PropagateConfig error = %v, want ErrNotAHub", err)
	}
}

func TestGetAndPutRouting(t *testing.T) {
	client, seen := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode":             "dispatch",
			"orgs":             map[string]string{"acme": "srv-a"},
			"repos":            map[string]string{"acme/tools": "hub-1"},
			"default_instance": "hub-1",
			"resolved_pool":    []string{"hub-1", "srv-a"},
			"enabled":          true,
		})
	})

	rules, err := client.GetRouting()
	if err != nil {
		t.Fatalf("GetRouting: %v", err)
	}
	if rules.Mode != "dispatch" || rules.Orgs["acme"] != "srv-a" || !rules.Enabled {
		t.Errorf("rules = %+v", rules)
	}

	if err := client.PutRouting(map[string]any{"repos": map[string]string{"acme/tools": "srv-a"}}); err != nil {
		t.Fatalf("PutRouting: %v", err)
	}
	last := (*seen)[len(*seen)-1]
	if !strings.HasPrefix(last, "PUT /cluster/routing") || !strings.Contains(last, "srv-a") {
		t.Errorf("last request = %q", last)
	}
}

// 207 is partial success, not a failure: one machine rebooting must not hide
// that the others were updated.
func TestPropagateConfigPartialSuccess(t *testing.T) {
	client, seen := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"failures":      1,
			"skipped_local": []string{"server.port"},
			"results": []map[string]any{
				{"instance_id": "srv-a", "ok": true, "applied_keys": []string{"ai.review_mode"}},
				{"instance_id": "srv-b", "ok": false, "error": "starting"},
			},
		})
	})

	report, err := client.PropagateConfig([]string{"srv-a", "srv-b"})
	if err != nil {
		t.Fatalf("PropagateConfig: %v", err)
	}
	if report.Failures != 1 || len(report.Results) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.SkippedLocal[0] != "server.port" {
		t.Errorf("skipped_local = %v", report.SkippedLocal)
	}
	if !strings.Contains((*seen)[0], "srv-b") {
		t.Errorf("targets were not sent: %v", *seen)
	}
}

func TestClusterErrorCarriesTheDaemonMessage(t *testing.T) {
	client, _ := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unknown instance \"ghost\""}`))
	})

	_, err := client.GetRouting()
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error = %v, want it to carry the daemon's message", err)
	}
}

func TestInstanceDisplayNameFallsBackToID(t *testing.T) {
	// A blank name must not render an empty column.
	for _, tc := range []struct{ name, id, want string }{
		{"Server A", "srv-a", "Server A"},
		{"", "srv-a", "srv-a"},
		{"   ", "srv-a", "srv-a"},
	} {
		got := api.Instance{ID: tc.id, Name: tc.name}.DisplayName()
		if got != tc.want {
			t.Errorf("DisplayName(%q/%q) = %q, want %q", tc.name, tc.id, got, tc.want)
		}
	}
}

func TestPutRoutingRejectsUnencodableBody(t *testing.T) {
	client, _ := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	// A channel cannot be JSON-encoded; the error has to surface locally rather
	// than sending a malformed body.
	err := client.PutRouting(map[string]any{"repos": make(chan int)})
	if err == nil || !strings.Contains(err.Error(), "encoding") {
		t.Errorf("PutRouting = %v, want an encoding error", err)
	}
}

func TestPropagateConfigWithoutTargets(t *testing.T) {
	var body string
	client, _ := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{"failures":0,"results":[]}`))
	})
	if _, err := client.PropagateConfig(nil); err != nil {
		t.Fatalf("PropagateConfig: %v", err)
	}
	// No targets means "every instance", which the daemon expresses as an
	// absent key rather than an empty list.
	if strings.Contains(body, "targets") {
		t.Errorf("body = %q, want no targets key", body)
	}
}

func TestPropagateConfigUnparseableBody(t *testing.T) {
	client, _ := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	if _, err := client.PropagateConfig(nil); err == nil {
		t.Error("PropagateConfig = nil error on an unparseable body")
	}
}

func TestClusterDecodeFailures(t *testing.T) {
	client, _ := newClusterServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not a daemon</html>"))
	})
	if _, err := client.ListInstances(); err == nil {
		t.Error("ListInstances = nil error on a non-JSON body")
	}
	if _, err := client.GetRouting(); err == nil {
		t.Error("GetRouting = nil error on a non-JSON body")
	}
}

func TestClusterUnreachableDaemon(t *testing.T) {
	// Port 1 on loopback refuses connections fast and deterministically.
	client := api.New("http://127.0.0.1:1", "tok")
	if _, err := client.ListInstances(); err == nil {
		t.Error("ListInstances = nil error against a dead host")
	}
}
