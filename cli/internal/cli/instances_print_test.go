package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestPrintInstances_EmptyRegistry(t *testing.T) {
	out := capture(t, func() {
		printInstances(&api.ClusterRegistry{SelfID: "hub-1"})
	})
	if !strings.Contains(out, "No instances registered") {
		t.Errorf("output = %q", out)
	}
}

func TestPrintInstances_ShowsHealthVersionAndShare(t *testing.T) {
	registry := &api.ClusterRegistry{
		Role: "hub", SelfID: "hub-1", SelfName: "Local hub",
		Instances: []api.Instance{
			{
				ID: "hub-1", Name: "Local hub", BaseURL: "http://127.0.0.1:7842",
				Enabled: true, Self: true, IsFallback: true, InPool: true,
				AssignedRepos: 2, Labels: []string{"macos"},
				State: &api.InstanceState{
					Reachable: true, Status: "ok", Version: "0.9.0", UptimeSeconds: 3700,
				},
			},
			{
				ID: "srv-a", Name: "Server A", BaseURL: "http://10.0.0.11:7842",
				Enabled: true, AssignedRepos: 1,
				State: &api.InstanceState{
					Reachable: false, LastError: "connection refused", ConsecutiveFailures: 3,
				},
			},
		},
	}

	out := capture(t, func() { printInstances(registry) })
	for _, want := range []string{
		"Local hub", "hub-1", "reachable", "version 0.9.0", "up 1h 1m",
		"2 repos routed", "macos", "[hub,default,pool]",
		// An unreachable instance must say why and for how long.
		"srv-a", "UNREACHABLE", "connection refused", "(3 failed probes)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestPrintInstances_ShowsTokenProblem(t *testing.T) {
	// An instance whose token cannot be resolved must say so rather than
	// looking merely unreachable.
	out := capture(t, func() {
		printInstances(&api.ClusterRegistry{
			SelfID: "hub-1",
			Instances: []api.Instance{{
				ID: "srv-a", Enabled: true, TokenError: "token_env HEIMDALLM_SRV_A is unset",
			}},
		})
	})
	if !strings.Contains(out, "token error") || !strings.Contains(out, "HEIMDALLM_SRV_A is unset") {
		t.Errorf("output = %q", out)
	}
}

func TestPrintInstances_MarksDisabled(t *testing.T) {
	out := capture(t, func() {
		printInstances(&api.ClusterRegistry{
			SelfID:    "hub-1",
			Instances: []api.Instance{{ID: "off", Enabled: false}},
		})
	})
	if !strings.Contains(out, "disabled") {
		t.Errorf("output = %q", out)
	}
}

func TestPrintInstances_NeverProbed(t *testing.T) {
	// Never probed is not the same as down; claiming otherwise right after a
	// hub restart would be misleading.
	out := capture(t, func() {
		printInstances(&api.ClusterRegistry{
			SelfID:    "hub-1",
			Instances: []api.Instance{{ID: "fresh", Enabled: true}},
		})
	})
	if !strings.Contains(out, "not probed") {
		t.Errorf("output = %q", out)
	}
}

// Strings from a remote daemon are semi-trusted; an ANSI escape in a terminal
// is a real injection vector.
func TestPrintInstances_SanitizesRemoteStrings(t *testing.T) {
	out := capture(t, func() {
		printInstances(&api.ClusterRegistry{
			SelfID: "hub-1",
			Instances: []api.Instance{{
				ID: "srv-a", Name: "evil\x1b[31mred", Enabled: true,
				State: &api.InstanceState{LastError: "boom\nsecond"},
			}},
		})
	})
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence survived: %q", out)
	}
}

func TestFormatUptime(t *testing.T) {
	tests := map[float64]string{
		45:    "45s",
		90:    "1m",
		3700:  "1h 1m",
		90000: "1d 1h",
		0:     "0s",
	}
	for seconds, want := range tests {
		if got := formatUptime(seconds); got != want {
			t.Errorf("formatUptime(%v) = %q, want %q", seconds, got, want)
		}
	}
}

func TestCapitalize(t *testing.T) {
	for in, want := range map[string]string{
		"":             "",
		"repository":   "Repository",
		"organization": "Organization",
		// Not title case: a single noun must not have every word capitalised.
		"pull request": "Pull request",
	} {
		if got := capitalize(in); got != want {
			t.Errorf("capitalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintRouting_Unconfigured(t *testing.T) {
	out := capture(t, func() { printRouting(&api.RoutingRules{}) })
	if !strings.Contains(out, "not configured") {
		t.Errorf("output = %q, want it to explain the single-daemon case", out)
	}
}

func TestPrintRouting_ShowsRules(t *testing.T) {
	out := capture(t, func() {
		printRouting(&api.RoutingRules{
			Enabled:         true,
			Mode:            "dispatch",
			DefaultInstance: "hub-1",
			ResolvedPool:    []string{"hub-1", "srv-a"},
			RoundRobinOps:   []string{"review", "merge"},
			Orgs:            map[string]string{"zulu": "srv-a", "acme": "hub-1"},
			Repos:           map[string]string{"acme/tools": "srv-a"},
		})
	})
	for _, want := range []string{
		"dispatch", "hub-1", "hub-1, srv-a", "review, merge",
		"Organizations", "acme", "zulu", "Repositories", "acme/tools",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// Sorted, so repeated runs read the same.
	if strings.Index(out, "acme") > strings.Index(out, "zulu") {
		t.Errorf("organizations are not sorted:\n%s", out)
	}
}

func TestPrintScope_SkipsEmpty(t *testing.T) {
	if out := capture(t, func() { printScope("Repositories", nil) }); out != "" {
		t.Errorf("printScope with no assignments printed %q", out)
	}
}

// contextWithClient produces the context PersistentPreRun would have built.
func contextWithClient(c *api.Client) context.Context {
	return context.WithValue(context.Background(), clientKey, c)
}

// clusterCmdServer runs a command against a fake daemon.
func clusterCmdServer(t *testing.T, handler http.HandlerFunc) *api.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return api.New(srv.URL, "tok")
}

func TestInstancesCmd_NotAHubIsNotAnError(t *testing.T) {
	// A plain single-daemon install answers 404 on the control plane. Exiting
	// non-zero there would break any script that runs this opportunistically.
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := newInstancesCmd()
	cmd.SetContext(contextWithClient(client))

	out := capture(t, func() {
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Errorf("RunE = %v, want nil", err)
		}
	})
	if !strings.Contains(out, "not a cluster hub") {
		t.Errorf("output = %q", out)
	}
}

func TestInstancesCmd_ListsFleet(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"self_id": "hub-1", "self_name": "Local hub",
			"instances": []map[string]any{
				{"id": "srv-a", "name": "Server A", "enabled": true, "assigned_repos": 3},
			},
		})
	})
	cmd := newInstancesCmd()
	cmd.SetContext(contextWithClient(client))

	out := capture(t, func() {
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("RunE = %v", err)
		}
	})
	if !strings.Contains(out, "Server A") || !strings.Contains(out, "3 repos routed") {
		t.Errorf("output = %q", out)
	}
}

func TestRoutingCmd_ShowsRules(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": true, "mode": "assignment", "default_instance": "hub-1",
			"repos": map[string]string{"acme/tools": "srv-a"},
		})
	})
	cmd := newRoutingCmd()
	cmd.SetContext(contextWithClient(client))

	out := capture(t, func() {
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("RunE = %v", err)
		}
	})
	if !strings.Contains(out, "acme/tools") || !strings.Contains(out, "srv-a") {
		t.Errorf("output = %q", out)
	}
}

func TestRoutingSetCmd_SendsTheWholeMap(t *testing.T) {
	var putBody string
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			putBody = string(body)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": true,
			"repos":   map[string]string{"acme/keep": "hub-1", "acme/tools": "hub-1"},
		})
	})
	cmd := newRoutingSetCmd("set-repo", "repository", "repos")
	cmd.SetContext(contextWithClient(client))

	out := capture(t, func() {
		if err := cmd.RunE(cmd, []string{"acme/tools", "srv-a"}); err != nil {
			t.Fatalf("RunE = %v", err)
		}
	})
	// PUT replaces the map wholesale, so the untouched rule has to be resent
	// or it would be deleted as a side effect.
	if !strings.Contains(putBody, "acme/keep") {
		t.Errorf("PUT body dropped an unrelated rule: %s", putBody)
	}
	if !strings.Contains(putBody, "srv-a") {
		t.Errorf("PUT body missing the new assignment: %s", putBody)
	}
	if !strings.Contains(out, "Repository acme/tools now routed to srv-a") {
		t.Errorf("output = %q", out)
	}
}

func TestRoutingSetCmd_ClearsARule(t *testing.T) {
	var putBody string
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			putBody = string(body)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": true,
			"repos":   map[string]string{"acme/tools": "srv-a"},
		})
	})
	cmd := newRoutingSetCmd("set-repo", "repository", "repos")
	cmd.SetContext(contextWithClient(client))

	out := capture(t, func() {
		if err := cmd.RunE(cmd, []string{"acme/tools"}); err != nil {
			t.Fatalf("RunE = %v", err)
		}
	})
	if strings.Contains(putBody, "acme/tools") {
		t.Errorf("the cleared rule was resent: %s", putBody)
	}
	if !strings.Contains(out, "inherits the default instance") {
		t.Errorf("output = %q", out)
	}
}

func TestPropagateConfigCmd_ReportsPerInstance(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"failures":      1,
			"skipped_local": []string{"server.port"},
			"results": []map[string]any{
				{"instance_id": "hub-1", "skipped": true, "ok": true, "error": "hub is the source of this config"},
				{"instance_id": "srv-a", "name": "Server A", "ok": true, "applied_keys": []string{"a", "b"}},
				{"instance_id": "srv-b", "ok": false, "error": "daemon is starting"},
			},
		})
	})
	cmd := newPropagateConfigCmd()
	cmd.SetContext(contextWithClient(client))

	var err error
	out := capture(t, func() { err = cmd.RunE(cmd, nil) })

	// A partial push is not a crash, but the exit code must say something went
	// wrong so a script notices.
	if err == nil || !strings.Contains(err.Error(), "1 instance") {
		t.Errorf("RunE = %v, want a failure count", err)
	}
	for _, want := range []string{
		"hub-1", "skipped", "Server A", "applied 2 settings",
		"srv-b", "daemon is starting", "Kept local: server.port",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestPropagateConfigCmd_AllOKExitsZero(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"failures": 0,
			"results":  []map[string]any{{"instance_id": "srv-a", "ok": true}},
		})
	})
	cmd := newPropagateConfigCmd()
	cmd.SetContext(contextWithClient(client))

	var err error
	capture(t, func() { err = cmd.RunE(cmd, nil) })
	if err != nil {
		t.Errorf("RunE = %v, want nil when every instance succeeded", err)
	}
}

func TestInstancesUseCmd(t *testing.T) {
	withConfig(t, `
[instances.srv-a]
host = "http://10.0.0.11:7842"
token = "a"
`)
	cmd := newInstancesUseCmd()

	out := capture(t, func() {
		if err := cmd.RunE(cmd, []string{"srv-a"}); err != nil {
			t.Fatalf("RunE = %v", err)
		}
	})
	if !strings.Contains(out, "srv-a") {
		t.Errorf("output = %q", out)
	}
	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultInstance != "srv-a" {
		t.Errorf("default_instance = %q, want it persisted", cfg.DefaultInstance)
	}

	// An unknown id must be refused with the configured options named.
	err = cmd.RunE(cmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "srv-a") {
		t.Errorf("RunE(ghost) = %v, want an error naming the choices", err)
	}
}

func TestRoutingCmd_NotAHub(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := newRoutingCmd()
	cmd.SetContext(contextWithClient(client))

	out := capture(t, func() {
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Errorf("RunE = %v, want nil on a non-hub", err)
		}
	})
	if !strings.Contains(out, "not a cluster hub") {
		t.Errorf("output = %q", out)
	}
}

func TestRoutingCmd_SurfacesRealErrors(t *testing.T) {
	// A 500 is a genuine failure and must not be swallowed the way a 404 is.
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cmd := newRoutingCmd()
	cmd.SetContext(contextWithClient(client))

	capture(t, func() {
		if err := cmd.RunE(cmd, nil); err == nil {
			t.Error("RunE = nil on a 500")
		}
	})
}

func TestRoutingSetCmd_NotAHub(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := newRoutingSetCmd("set-org", "organization", "orgs")
	cmd.SetContext(contextWithClient(client))

	out := capture(t, func() {
		if err := cmd.RunE(cmd, []string{"acme", "srv-a"}); err != nil {
			t.Errorf("RunE = %v", err)
		}
	})
	if !strings.Contains(out, "not a cluster hub") {
		t.Errorf("output = %q", out)
	}
}

func TestRoutingSetCmd_ReportsAWriteFailure(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unknown instance \"ghost\""}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true})
	})
	cmd := newRoutingSetCmd("set-repo", "repository", "repos")
	cmd.SetContext(contextWithClient(client))

	capture(t, func() {
		err := cmd.RunE(cmd, []string{"acme/tools", "ghost"})
		if err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Errorf("RunE = %v, want the daemon's reason", err)
		}
	})
}

func TestPropagateConfigCmd_NotAHub(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cmd := newPropagateConfigCmd()
	cmd.SetContext(contextWithClient(client))

	out := capture(t, func() {
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Errorf("RunE = %v", err)
		}
	})
	if !strings.Contains(out, "not a cluster hub") {
		t.Errorf("output = %q", out)
	}
}

func TestInstancesCmd_SurfacesRealErrors(t *testing.T) {
	client := clusterCmdServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cmd := newInstancesCmd()
	cmd.SetContext(contextWithClient(client))

	capture(t, func() {
		if err := cmd.RunE(cmd, nil); err == nil {
			t.Error("RunE = nil on a 500")
		}
	})
}

func TestInstancesUseCmd_NoConfigFile(t *testing.T) {
	// Without a config file there is nothing to select, and the error has to
	// say which file it looked at.
	withConfig(t, "")
	cmd := newInstancesUseCmd()
	err := cmd.RunE(cmd, []string{"srv-a"})
	if err == nil {
		t.Fatal("RunE = nil with no config file")
	}
	if !strings.Contains(err.Error(), "cli.toml") && !strings.Contains(err.Error(), "srv-a") {
		t.Errorf("error = %v, want it to name the file or the choices", err)
	}
}
