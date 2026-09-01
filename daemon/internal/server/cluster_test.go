package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/instances"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/store"
)

// ---------------------------------------------------------------- test doubles

// fakeInstance is an httptest server standing in for a remote daemon.
type fakeInstance struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	tokens   []string
	bodies   []string
}

func newFakeInstance(t *testing.T, id string, handler http.HandlerFunc) *fakeInstance {
	t.Helper()
	f := &fakeInstance{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.tokens = append(f.tokens, r.Header.Get(instances.HeaderToken))
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "version": "0.9.0", "instance_id": id,
				"instance_name": "Fake " + id, "role": "worker",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "served_by": id})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeInstance) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeInstance) lastToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokens) == 0 {
		return ""
	}
	return f.tokens[len(f.tokens)-1]
}

// fakeClusterStore records dispatch claims without a database.
type fakeClusterStore struct {
	mu       sync.Mutex
	claims   map[string]string
	deleted  []string
	claimErr error
}

func newFakeClusterStore() *fakeClusterStore {
	return &fakeClusterStore{claims: map[string]string{}}
}

func (f *fakeClusterStore) key(op, target, sha string) string {
	return op + "|" + target + "|" + sha
}

func (f *fakeClusterStore) ClaimDispatch(op, target, sha, instanceID string) (bool, error) {
	if f.claimErr != nil {
		return false, f.claimErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(op, target, sha)
	if _, exists := f.claims[k]; exists {
		return false, nil
	}
	f.claims[k] = instanceID
	return true, nil
}

func (f *fakeClusterStore) DispatchTarget(op, target, sha string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.claims[f.key(op, target, sha)]
	return id, ok, nil
}

func (f *fakeClusterStore) DeleteInstanceState(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

// ------------------------------------------------------------------- fixtures

const testToken = "hub-token"

type hubFixture struct {
	srv        *server.Server
	configPath string
	store      *fakeClusterStore
	remotes    map[string]*fakeInstance
	cfg        *config.Config
	// router is built once and Update()d on reload, never rebuilt per request.
	// Rebuilding it would reset the round-robin counters, so every operation
	// would go to the first instance in the pool.
	router *instances.Router
	mu     sync.Mutex
}

// newHub builds a hub Server backed by a real config.toml on disk, so the
// TOML read-merge-validate-write path is exercised end to end rather than
// mocked out.
func newHub(t *testing.T, remotes map[string]*fakeInstance, routing config.RoutingConfig) *hubFixture {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	var b strings.Builder
	b.WriteString("[ai]\nprimary = \"claude\"\n\n[cluster]\nrole = \"hub\"\ninstance_id = \"hub-1\"\ninstance_name = \"Local hub\"\ndefault_instance = \"hub-1\"\n\n")
	b.WriteString("[cluster.instances.hub-1]\nname = \"Local hub\"\nbase_url = \"http://127.0.0.1:7842\"\ntoken = \"hub-token\"\n\n")
	for id, remote := range remotes {
		fmt.Fprintf(&b, "[cluster.instances.%s]\nname = %q\nbase_url = %q\ntoken = \"remote-%s\"\n\n", id, "Remote "+id, remote.URL, id)
	}
	if routing.Mode != "" || len(routing.RoundRobinPool) > 0 || len(routing.RoundRobinOps) > 0 ||
		len(routing.Orgs) > 0 || len(routing.Repos) > 0 {
		b.WriteString("[cluster.routing]\n")
		if routing.Mode != "" {
			fmt.Fprintf(&b, "mode = %q\n", routing.Mode)
		}
		if len(routing.RoundRobinPool) > 0 {
			fmt.Fprintf(&b, "round_robin_pool = [%s]\n", quoteList(routing.RoundRobinPool))
		}
		if len(routing.RoundRobinOps) > 0 {
			fmt.Fprintf(&b, "round_robin_ops = [%s]\n", quoteList(routing.RoundRobinOps))
		}
		b.WriteString("\n")
		if len(routing.Orgs) > 0 {
			b.WriteString("[cluster.routing.orgs]\n")
			for org, id := range routing.Orgs {
				fmt.Fprintf(&b, "%q = %q\n", org, id)
			}
			b.WriteString("\n")
		}
		if len(routing.Repos) > 0 {
			b.WriteString("[cluster.routing.repos]\n")
			for repo, id := range routing.Repos {
				fmt.Fprintf(&b, "%q = %q\n", repo, id)
			}
			b.WriteString("\n")
		}
	}
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := server.New(st, nil, nil, testToken)
	srv.SetConfigPath(cfgPath)

	f := &hubFixture{srv: srv, configPath: cfgPath, store: newFakeClusterStore(), remotes: remotes}
	f.reload(t)

	srv.SetClusterIdentity("hub-1", "Local hub", config.RoleHub)
	srv.SetCluster(&server.ClusterDeps{
		Snapshot: f.snapshot,
		Store:    f.store,
		NewClient: func(inst instances.Instance) *instances.Client {
			for id, remote := range remotes {
				if inst.ID == id {
					return instances.NewClient(inst, remote.Client())
				}
			}
			return instances.NewClient(inst, nil)
		},
	})
	srv.SetConfigFn(func() map[string]any {
		f.mu.Lock()
		defer f.mu.Unlock()
		return map[string]any{
			"review_mode":   f.cfg.AI.ReviewMode,
			"poll_interval": f.cfg.GitHub.PollInterval,
			"server_port":   f.cfg.Server.Port,
		}
	})
	srv.SetReloadFn(func() error { f.reload(t); return nil })
	return f
}

func quoteList(in []string) string {
	parts := make([]string, 0, len(in))
	for _, s := range in {
		parts = append(parts, fmt.Sprintf("%q", s))
	}
	return strings.Join(parts, ", ")
}

func (f *hubFixture) reload(t *testing.T) {
	t.Helper()
	cfg, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	reg := instances.NewRegistry(cfg)
	f.mu.Lock()
	f.cfg = cfg
	if f.router == nil {
		f.router = instances.NewRouter(reg, cfg)
	} else {
		f.router.Update(reg, cfg)
	}
	f.mu.Unlock()
}

func (f *hubFixture) snapshot() server.ClusterSnapshot {
	f.mu.Lock()
	cfg, router := f.cfg, f.router
	f.mu.Unlock()
	reg := instances.NewRegistry(cfg)
	factory := func(inst instances.Instance) *instances.Client {
		if remote, ok := f.remotes[inst.ID]; ok {
			return instances.NewClient(inst, remote.Client())
		}
		return instances.NewClient(inst, nil)
	}
	return server.ClusterSnapshot{
		Registry:   reg,
		Router:     router,
		Propagator: instances.NewPropagator(reg, factory),
		Role:       cfg.Cluster.Role,
		SelfID:     cfg.Cluster.InstanceID,
		SelfName:   cfg.Cluster.InstanceName,
	}
}

func (f *hubFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(instances.HeaderToken, testToken)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// ----------------------------------------------------------------------- tests

// A daemon with no cluster wiring must look like it never had the capability,
// not like it has it switched off.
func TestClusterRoutesAbsentWithoutHub(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := server.New(st, nil, nil, testToken)

	for _, path := range []string{
		"/instances", "/cluster/routing", "/cluster/drift",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(instances.HeaderToken, testToken)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 on a non-hub daemon", path, rec.Code)
		}
	}
}

func TestClusterRoutesRequireAuth(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/instances"},
		{http.MethodGet, "/cluster/routing"},
		{http.MethodGet, "/cluster/drift"},
		{http.MethodPost, "/instances"},
		{http.MethodPut, "/cluster/routing"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		f.srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestHealthReportsClusterIdentity(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	// /health is unauthenticated on purpose: it is how a hub learns what an
	// instance calls itself before it holds a token for it.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)

	body := decode(t, rec)
	if body["instance_id"] != "hub-1" || body["instance_name"] != "Local hub" || body["role"] != config.RoleHub {
		t.Errorf("/health = %v, want the cluster identity", body)
	}
}

func TestHealthOmitsClusterIdentityWhenUnset(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv := server.New(st, nil, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	body := decode(t, rec)
	for _, key := range []string{"instance_id", "instance_name", "role"} {
		if _, present := body[key]; present {
			t.Errorf("/health on a standalone daemon includes %q; want it omitted", key)
		}
	}
}

func TestListInstances(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": remote}, config.RoutingConfig{
		Repos: map[string]string{"acme/tools": "srv-a", "acme/other": "srv-a"},
	})

	rec := f.do(t, http.MethodGet, "/instances", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /instances = %d: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["self_id"] != "hub-1" || body["role"] != config.RoleHub {
		t.Errorf("listing = %v, want the hub identity", body)
	}
	list, _ := body["instances"].([]any)
	if len(list) != 2 {
		t.Fatalf("got %d instances, want 2", len(list))
	}

	byID := map[string]map[string]any{}
	for _, entry := range list {
		m := entry.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if byID["hub-1"]["self"] != true {
		t.Error("hub-1 should be marked self")
	}
	if byID["srv-a"]["assigned_repos"].(float64) != 2 {
		t.Errorf("srv-a assigned_repos = %v, want 2", byID["srv-a"]["assigned_repos"])
	}
	// Tokens are the one thing that must never appear in an API response.
	if strings.Contains(rec.Body.String(), "remote-srv-a") || strings.Contains(rec.Body.String(), "hub-token") {
		t.Error("GET /instances leaked an API token")
	}
}

func TestRegisterInstanceProbesAndPersists(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	newcomer := newFakeInstance(t, "srv-b", nil)

	// The fixture's client factory only knows the remotes it was built with,
	// so point the registration probe at this one explicitly.
	f.srv.SetCluster(&server.ClusterDeps{
		Snapshot: f.snapshot,
		Store:    f.store,
		NewClient: func(inst instances.Instance) *instances.Client {
			return instances.NewClient(inst, newcomer.Client())
		},
	})

	rec := f.do(t, http.MethodPost, "/instances",
		fmt.Sprintf(`{"base_url":%q,"token":"remote-secret"}`, newcomer.URL))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /instances = %d: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	// The id the instance reports for itself is adopted, so both sides use the
	// same name in their logs.
	if body["id"] != "srv-b" {
		t.Errorf("id = %v, want the instance's self-reported srv-b", body["id"])
	}

	raw, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "srv-b") || !strings.Contains(string(raw), "remote-secret") {
		t.Errorf("config.toml was not updated:\n%s", raw)
	}
	if !containsAny(newcomer.seen(), "GET /health") {
		t.Errorf("registration did not probe the instance: %v", newcomer.seen())
	}
}

func TestRegisterInstanceRejectsUnreachable(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	// Registering something that never answers would leave an entry that looks
	// fine and silently never works.
	rec := f.do(t, http.MethodPost, "/instances", `{"base_url":"http://127.0.0.1:1","token":"t"}`)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("POST /instances for an unreachable host = %d, want 502", rec.Code)
	}
	raw, _ := os.ReadFile(f.configPath)
	if strings.Contains(string(raw), "127.0.0.1:1") {
		t.Error("an unreachable instance was written to config.toml anyway")
	}
}

func TestRegisterInstanceValidation(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	tests := map[string]string{
		"bad json":       `{`,
		"missing url":    `{"token":"t"}`,
		"bad scheme":     `{"base_url":"file:///etc/passwd","token":"t"}`,
		"credentials":    `{"base_url":"http://u:p@host:7842","token":"t"}`,
		"no token":       `{"base_url":"http://127.0.0.1:7999"}`,
		"two token srcs": `{"base_url":"http://127.0.0.1:7999","token":"t","token_env":"X"}`,
		"bad id":         `{"id":"has/slash","base_url":"http://127.0.0.1:7999","token":"t","skip_probe":true}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, http.MethodPost, "/instances", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST /instances = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
		})
	}
}

func TestRegisterInstanceRejectsDuplicate(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": remote}, config.RoutingConfig{})
	rec := f.do(t, http.MethodPost, "/instances",
		fmt.Sprintf(`{"id":"srv-a","base_url":%q,"token":"t","skip_probe":true}`, remote.URL))
	if rec.Code != http.StatusConflict {
		t.Errorf("re-registering srv-a = %d, want 409", rec.Code)
	}
}

func TestPatchInstance(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": remote}, config.RoutingConfig{})

	rec := f.do(t, http.MethodPatch, "/instances/srv-a", `{"name":"Renamed","enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}
	raw, _ := os.ReadFile(f.configPath)
	if !strings.Contains(string(raw), "Renamed") || !strings.Contains(string(raw), "enabled = false") {
		t.Errorf("config.toml missing the patch:\n%s", raw)
	}

	if rec := f.do(t, http.MethodPatch, "/instances/ghost", `{"name":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("PATCH on an unknown instance = %d, want 404", rec.Code)
	}
	if rec := f.do(t, http.MethodPatch, "/instances/srv-a", `{"base_url":"nonsense"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with a bad base_url = %d, want 400", rec.Code)
	}
}

// Rotating to a different token source must clear the previous one, or the
// config would declare two and fail validation on the next load.
func TestPatchInstanceSwitchesTokenSource(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": remote}, config.RoutingConfig{})

	rec := f.do(t, http.MethodPatch, "/instances/srv-a", `{"token_env":"HEIMDALLM_SRV_A"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}
	raw, _ := os.ReadFile(f.configPath)
	if strings.Contains(string(raw), `token = "remote-srv-a"`) {
		t.Errorf("the inline token survived a switch to token_env:\n%s", raw)
	}
	if !strings.Contains(string(raw), "HEIMDALLM_SRV_A") {
		t.Errorf("token_env was not written:\n%s", raw)
	}
	// Proof the result still loads: two declared sources would be rejected.
	if _, err := config.Load(f.configPath); err != nil {
		t.Errorf("config.toml no longer loads after the token switch: %v", err)
	}
}

func TestDeleteInstanceRemovesReferences(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": remote}, config.RoutingConfig{
		Mode:           config.ModeAssignment,
		RoundRobinPool: []string{"hub-1", "srv-a"},
		Orgs:           map[string]string{"acme": "srv-a"},
		Repos:          map[string]string{"acme/tools": "srv-a"},
	})

	rec := f.do(t, http.MethodDelete, "/instances/srv-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
	}

	raw, _ := os.ReadFile(f.configPath)
	text := string(raw)
	// Rules left pointing at a removed instance would fail validation on the
	// very next load, so the removal has to take its references with it.
	if strings.Contains(text, "srv-a") {
		t.Errorf("references to srv-a survived deletion:\n%s", text)
	}
	if _, err := config.Load(f.configPath); err != nil {
		t.Errorf("config.toml no longer loads after deletion: %v", err)
	}
	if len(f.store.deleted) != 1 || f.store.deleted[0] != "srv-a" {
		t.Errorf("observed state was not cleaned up: %v", f.store.deleted)
	}
}

func TestDeleteInstanceRefusesSelf(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	if rec := f.do(t, http.MethodDelete, "/instances/hub-1", ""); rec.Code != http.StatusConflict {
		t.Errorf("deleting the hub itself = %d, want 409", rec.Code)
	}
	if rec := f.do(t, http.MethodDelete, "/instances/ghost", ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleting an unknown instance = %d, want 404", rec.Code)
	}
}

func TestGetAndPutRouting(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": remote}, config.RoutingConfig{})

	rec := f.do(t, http.MethodPut, "/cluster/routing",
		`{"mode":"dispatch","round_robin_pool":["hub-1","srv-a"],"round_robin_ops":["review"],`+
			`"orgs":{"acme":"srv-a"},"repos":{"acme/tools":"hub-1"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /cluster/routing = %d: %s", rec.Code, rec.Body)
	}

	rec = f.do(t, http.MethodGet, "/cluster/routing", "")
	body := decode(t, rec)
	if body["mode"] != config.ModeDispatch {
		t.Errorf("mode = %v, want dispatch", body["mode"])
	}
	orgs := body["orgs"].(map[string]any)
	if orgs["acme"] != "srv-a" {
		t.Errorf("orgs = %v, want acme -> srv-a", orgs)
	}
	if body["enabled"] != true {
		t.Error("routing should report itself enabled once rules exist")
	}
}

func TestPutRoutingValidatesReferences(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": remote}, config.RoutingConfig{})

	// A typo must be a 400 naming the bad id, not a 500 from the config
	// validator after the file was already rewritten.
	tests := map[string]string{
		"unknown org target":  `{"orgs":{"acme":"ghost"}}`,
		"unknown repo target": `{"repos":{"acme/tools":"ghost"}}`,
		"unknown pool member": `{"round_robin_pool":["ghost"]}`,
		"unknown default":     `{"default_instance":"ghost"}`,
		"bad org slug":        `{"orgs":{"not a slug":"srv-a"}}`,
		"bad repo slug":       `{"repos":{"../escape":"srv-a"}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, http.MethodPut, "/cluster/routing", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("PUT = %d, want 400 (%s)", rec.Code, rec.Body)
			}
		})
	}
	// None of the rejected writes may have touched the file.
	if _, err := config.Load(f.configPath); err != nil {
		t.Errorf("config.toml damaged by rejected writes: %v", err)
	}
}

// PUT replaces maps wholesale; merging would make deleting a rule impossible.
func TestPutRoutingReplacesMaps(t *testing.T) {
	remote := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": remote}, config.RoutingConfig{
		Repos: map[string]string{"acme/old": "srv-a"},
	})

	if rec := f.do(t, http.MethodPut, "/cluster/routing", `{"repos":{"acme/new":"srv-a"}}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body)
	}
	body := decode(t, f.do(t, http.MethodGet, "/cluster/routing", ""))
	repos := body["repos"].(map[string]any)
	if _, stale := repos["acme/old"]; stale {
		t.Errorf("repos = %v, want the old rule replaced not merged", repos)
	}
	if repos["acme/new"] != "srv-a" {
		t.Errorf("repos = %v, want the new rule", repos)
	}
}

func TestPropagateSkipsLocalKeysAndReportsPerInstance(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	b := newFakeInstance(t, "srv-b", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "instance_id": "srv-b"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	f := newHub(t, map[string]*fakeInstance{"srv-a": a, "srv-b": b}, config.RoutingConfig{})

	rec := f.do(t, http.MethodPost, "/cluster/propagate", "")
	// One instance failing must not stop the other from being updated, and the
	// caller needs the per-instance reasons either way.
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("POST /cluster/propagate = %d, want 207: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["failures"].(float64) != 1 {
		t.Errorf("failures = %v, want 1", body["failures"])
	}

	results := map[string]map[string]any{}
	for _, entry := range body["results"].([]any) {
		m := entry.(map[string]any)
		results[m["instance_id"].(string)] = m
	}
	if results["hub-1"]["skipped"] != true {
		t.Errorf("hub = %v, want skipped as the source of truth", results["hub-1"])
	}
	if results["srv-a"]["ok"] != true {
		t.Errorf("srv-a = %v, want applied", results["srv-a"])
	}
	if results["srv-b"]["ok"] == true {
		t.Errorf("srv-b = %v, want a failure", results["srv-b"])
	}

	// The machine-specific keys must not have reached the wire.
	for _, sent := range a.bodies {
		for _, forbidden := range []string{"hub-token", "remote-srv-a", "cluster", "instance_id", "7842"} {
			if strings.Contains(sent, forbidden) {
				t.Errorf("propagated body leaked %q: %s", forbidden, sent)
			}
		}
	}
}

func TestPropagateRejectsUnknownTargets(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": a}, config.RoutingConfig{})
	rec := f.do(t, http.MethodPost, "/cluster/propagate", `{"targets":["ghost"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("propagating to only unknown targets = %d, want 400", rec.Code)
	}
}

func TestConfigDrift(t *testing.T) {
	a := newFakeInstance(t, "srv-a", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/config" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"review_mode":   "multi", // differs from the hub's default "single"
				"poll_interval": "5m",
				"server_port":   9999, // local: must not be reported
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	f := newHub(t, map[string]*fakeInstance{"srv-a": a}, config.RoutingConfig{})

	rec := f.do(t, http.MethodGet, "/cluster/drift", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /cluster/drift = %d: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	var srvA map[string]any
	for _, entry := range body["instances"].([]any) {
		m := entry.(map[string]any)
		if m["instance_id"] == "srv-a" {
			srvA = m
		}
	}
	if srvA == nil {
		t.Fatalf("no drift entry for srv-a: %s", rec.Body)
	}
	keys := map[string]bool{}
	for _, d := range srvA["drifts"].([]any) {
		keys[d.(map[string]any)["key"].(string)] = true
	}
	if !keys["review_mode"] {
		t.Error("review_mode differs but was not reported")
	}
	if keys["poll_interval"] {
		t.Error("poll_interval matches but was reported as drift")
	}
	// A differing port is a different machine, not configuration drift.
	if keys["server_port"] {
		t.Error("server_port is machine-specific and must not be reported as drift")
	}
}

func TestDispatchRoutesToRepoOwner(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": a}, config.RoutingConfig{
		Mode:  config.ModeAssignment,
		Repos: map[string]string{"acme/tools": "srv-a"},
	})

	rec := f.do(t, http.MethodPost, "/cluster/dispatch/review",
		`{"pr_id":42,"repo":"acme/tools","number":7,"head_sha":"sha1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("dispatch = %d: %s", rec.Code, rec.Body)
	}
	if decode(t, rec)["instance_id"] != "srv-a" {
		t.Errorf("dispatch went to %v, want the repo owner srv-a", decode(t, rec)["instance_id"])
	}
	if !containsAny(a.seen(), "POST /prs/42/review") {
		t.Errorf("srv-a requests = %v, want the review trigger", a.seen())
	}
	// The instance's own token must be used, never the caller's.
	if a.lastToken() != "remote-srv-a" {
		t.Errorf("token forwarded = %q, want the instance's own", a.lastToken())
	}
}

func TestDispatchRoundRobinsInDispatchMode(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	b := newFakeInstance(t, "srv-b", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": a, "srv-b": b}, config.RoutingConfig{
		Mode:           config.ModeDispatch,
		RoundRobinPool: []string{"srv-a", "srv-b"},
		RoundRobinOps:  []string{config.OpReview},
	})

	var picks []string
	for i := 0; i < 4; i++ {
		rec := f.do(t, http.MethodPost, "/cluster/dispatch/review",
			fmt.Sprintf(`{"pr_id":%d,"repo":"acme/tools","number":%d,"head_sha":"sha%d"}`, i, i, i))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("dispatch %d = %d: %s", i, rec.Code, rec.Body)
		}
		picks = append(picks, decode(t, rec)["instance_id"].(string))
	}
	if picks[0] == picks[1] {
		t.Errorf("picks = %v, want consecutive operations spread across the pool", picks)
	}
	counts := map[string]int{}
	for _, p := range picks {
		counts[p]++
	}
	if counts["srv-a"] != 2 || counts["srv-b"] != 2 {
		t.Errorf("distribution = %v, want an even 2/2", counts)
	}
}

// A retry, or two GUI clients clicking at once, must not send the same work
// twice; a new head SHA is genuinely a new operation.
func TestDispatchDeduplicatesPerCommit(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": a}, config.RoutingConfig{
		Repos: map[string]string{"acme/tools": "srv-a"},
	})

	body := `{"pr_id":42,"repo":"acme/tools","number":7,"head_sha":"sha1"}`
	if rec := f.do(t, http.MethodPost, "/cluster/dispatch/review", body); rec.Code != http.StatusAccepted {
		t.Fatalf("first dispatch = %d: %s", rec.Code, rec.Body)
	}
	rec := f.do(t, http.MethodPost, "/cluster/dispatch/review", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate dispatch = %d, want 200: %s", rec.Code, rec.Body)
	}
	if decode(t, rec)["duplicate"] != true {
		t.Errorf("duplicate dispatch = %v, want it reported as a duplicate", decode(t, rec))
	}
	if got := countRequests(a.seen(), "POST /prs/42/review"); got != 1 {
		t.Errorf("the instance received %d reviews, want exactly 1", got)
	}

	// A new push must claim cleanly.
	newSHA := `{"pr_id":42,"repo":"acme/tools","number":7,"head_sha":"sha2"}`
	if rec := f.do(t, http.MethodPost, "/cluster/dispatch/review", newSHA); rec.Code != http.StatusAccepted {
		t.Errorf("dispatch for a new head SHA = %d, want 202", rec.Code)
	}
}

func TestDispatchValidation(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": a}, config.RoutingConfig{})

	if rec := f.do(t, http.MethodPost, "/cluster/dispatch/deploy", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown op = %d, want 400", rec.Code)
	}
	if rec := f.do(t, http.MethodPost, "/cluster/dispatch/review", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON = %d, want 400", rec.Code)
	}
	if rec := f.do(t, http.MethodPost, "/cluster/dispatch/review", `{"repo":"../escape"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad repo slug = %d, want 400", rec.Code)
	}
	if rec := f.do(t, http.MethodPost, "/cluster/dispatch/review", `{"instance":"ghost"}`); rec.Code != http.StatusNotFound {
		t.Errorf("forced unknown instance = %d, want 404", rec.Code)
	}
}

func TestProxyForwardsWithInstanceToken(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": a}, config.RoutingConfig{})
	f.srv.SetCluster(&server.ClusterDeps{
		Snapshot:   f.snapshot,
		Store:      f.store,
		HTTPClient: a.Client(),
	})

	rec := f.do(t, http.MethodGet, "/instances/srv-a/proxy/prs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "srv-a") {
		t.Errorf("proxy body = %s, want the instance's response", rec.Body)
	}
	if !containsAny(a.seen(), "GET /prs") {
		t.Errorf("instance requests = %v, want GET /prs", a.seen())
	}
	// The caller authenticated to the hub; the instance has its own token and
	// the caller's must never be forwarded.
	if a.lastToken() != "remote-srv-a" {
		t.Errorf("forwarded token = %q, want the instance's own token", a.lastToken())
	}
}

func TestProxySecurity(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": a}, config.RoutingConfig{})
	f.srv.SetCluster(&server.ClusterDeps{
		Snapshot:   f.snapshot,
		Store:      f.store,
		HTTPClient: a.Client(),
	})

	t.Run("unknown instance", func(t *testing.T) {
		// The target is always looked up in the registry by id, never taken
		// from the request, so there is no URL a client can supply.
		if rec := f.do(t, http.MethodGet, "/instances/ghost/proxy/prs", ""); rec.Code != http.StatusNotFound {
			t.Errorf("= %d, want 404", rec.Code)
		}
	})

	t.Run("denied paths", func(t *testing.T) {
		// An allowlist: /shutdown has no supervisor to bring a remote daemon
		// back, /update/* is bound to a single process, and nested proxying is
		// simply not a capability anyone asked for.
		for _, path := range []string{
			"/instances/srv-a/proxy/shutdown",
			"/instances/srv-a/proxy/update/prepare",
			"/instances/srv-a/proxy/admin/repo-rename",
			"/instances/srv-a/proxy/instances/srv-a/proxy/prs",
			"/instances/srv-a/proxy/cluster/routing",
		} {
			rec := f.do(t, http.MethodPost, path, "{}")
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s = %d, want 403", path, rec.Code)
			}
		}
		if len(a.seen()) != 0 {
			t.Errorf("denied paths still reached the instance: %v", a.seen())
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/instances/srv-a/proxy/prs", nil)
		rec := httptest.NewRecorder()
		f.srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated proxy = %d, want 401", rec.Code)
		}
	})
}

// The hub serves its own data directly rather than looping back over the
// network to itself.
func TestProxyToSelfIsServedLocally(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	rec := f.do(t, http.MethodGet, "/instances/hub-1/proxy/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("self proxy = %d: %s", rec.Code, rec.Body)
	}
	if decode(t, rec)["instance_id"] != "hub-1" {
		t.Errorf("self proxy body = %s, want the hub's own health", rec.Body)
	}
}

func TestProxyReportsUnreachableInstance(t *testing.T) {
	dead := newFakeInstance(t, "dead", nil)
	f := newHub(t, map[string]*fakeInstance{"dead": dead}, config.RoutingConfig{})
	dead.Close() // the registry entry survives; the process does not

	rec := f.do(t, http.MethodGet, "/instances/dead/proxy/prs", "")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("proxy to a dead instance = %d, want 502", rec.Code)
	}
}

func TestProbeInstanceEndpointRequiresProber(t *testing.T) {
	a := newFakeInstance(t, "srv-a", nil)
	f := newHub(t, map[string]*fakeInstance{"srv-a": a}, config.RoutingConfig{})
	// The fixture wires no Prober, so the endpoint must say so rather than
	// pretend it probed.
	if rec := f.do(t, http.MethodPost, "/instances/srv-a/probe", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("probe without a prober = %d, want 503", rec.Code)
	}
}

func containsAny(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func countRequests(haystack []string, needle string) int {
	n := 0
	for _, s := range haystack {
		if s == needle {
			n++
		}
	}
	return n
}
