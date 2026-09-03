package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/instances"
)

// ClusterSnapshot is the control plane's view at one instant. main rebuilds it
// on every config reload so handlers never hold a stale registry.
//
// IMPORTANT: Router must be the SAME instance across snapshots, updated in
// place via Router.Update on reload — never a fresh NewRouter per call. The
// round-robin counters live inside it, so rebuilding it per request resets the
// rotation and sends every single operation to the first instance in the pool.
// Registry and Propagator are immutable snapshots and are cheap to rebuild.
type ClusterSnapshot struct {
	Registry   *instances.Registry
	Router     *instances.Router
	Propagator *instances.Propagator
	Role       string
	SelfID     string
	SelfName   string
}

// ClusterStore is the slice of the store the control plane needs. Narrow on
// purpose so tests can supply a fake without a database.
type ClusterStore interface {
	ClaimDispatch(op, targetKey, headSHA, instanceID string) (bool, error)
	DispatchTarget(op, targetKey, headSHA string) (string, bool, error)
	ReleaseDispatch(op, targetKey, headSHA string) error
	DeleteInstanceState(instanceID string) error
}

// ClusterDeps wires the hub's control plane into the HTTP server. A nil
// ClusterDeps means this daemon is not a hub and none of the /instances or
// /cluster routes are mounted at all — not merely disabled, absent, so a worker
// cannot be driven as if it were a hub.
type ClusterDeps struct {
	// Snapshot returns the live registry, router and propagator.
	Snapshot func() ClusterSnapshot
	// Prober supplies observed health. May be nil in tests.
	Prober *instances.Prober
	// Store persists dispatch claims and instance state.
	Store ClusterStore
	// NewClient builds a client for an instance. Nil uses the real HTTP client.
	NewClient instances.ClientFactory
	// HTTPClient is used by the reverse proxy. Nil uses http.DefaultTransport.
	HTTPClient *http.Client
}

func (d *ClusterDeps) clientFor(inst instances.Instance) *instances.Client {
	if d.NewClient != nil {
		return d.NewClient(inst)
	}
	return instances.NewClient(inst, nil)
}

// SetCluster installs the control plane. Call before Serve.
func (srv *Server) SetCluster(deps *ClusterDeps) {
	srv.clusterMu.Lock()
	defer srv.clusterMu.Unlock()
	srv.cluster = deps
}

// SetClusterIdentity records this daemon's own identity for GET /health.
//
// Deliberately independent of ClusterDeps: a worker is not a hub and has no
// control plane, but the hub still needs to read its id, name and role off the
// one unauthenticated route in order to recognise it.
func (srv *Server) SetClusterIdentity(id, name, role string) {
	srv.clusterMu.Lock()
	defer srv.clusterMu.Unlock()
	srv.instanceID, srv.instanceName, srv.clusterRole = id, name, role
}

func (srv *Server) clusterIdentity() (id, name, role string) {
	srv.clusterMu.RLock()
	defer srv.clusterMu.RUnlock()
	return srv.instanceID, srv.instanceName, srv.clusterRole
}

func (srv *Server) clusterDeps() *ClusterDeps {
	srv.clusterMu.RLock()
	defer srv.clusterMu.RUnlock()
	return srv.cluster
}

// snapshot returns the current control-plane view, or false when this daemon is
// not a hub.
func (srv *Server) snapshot() (ClusterSnapshot, bool) {
	deps := srv.clusterDeps()
	if deps == nil || deps.Snapshot == nil {
		return ClusterSnapshot{}, false
	}
	return deps.Snapshot(), true
}

// mountClusterRoutes registers the control plane.
//
// The routes are always registered — the router is built once in the
// constructor, before main can call SetCluster, and rebuilding it later would
// race with in-flight requests. hubOnly instead makes them answer 404 on a
// daemon that is not a hub, so a worker looks like it never had the capability
// rather than like it has it switched off.
func (srv *Server) mountClusterRoutes(r chi.Router) {
	r.Get("/instances", srv.hubOnly(srv.handleListInstances))
	r.Post("/instances", srv.hubOnly(srv.handleRegisterInstance))
	r.Patch("/instances/{id}", srv.hubOnly(srv.handlePatchInstance))
	r.Delete("/instances/{id}", srv.hubOnly(srv.handleDeleteInstance))
	r.Post("/instances/{id}/probe", srv.hubOnly(srv.handleProbeInstance))
	r.Handle("/instances/{id}/proxy/*", srv.hubOnly(srv.handleInstanceProxy))

	r.Get("/cluster/routing", srv.hubOnly(srv.handleGetRouting))
	r.Put("/cluster/routing", srv.hubOnly(srv.handlePutRouting))
	r.Get("/cluster/drift", srv.hubOnly(srv.handleConfigDrift))
	r.Post("/cluster/propagate", srv.hubOnly(srv.handlePropagateConfig))
	r.Post("/cluster/dispatch/{op}", srv.hubOnly(srv.handleDispatch))
}

// hubOnly hides a control-plane route on a daemon with no cluster wiring.
func (srv *Server) hubOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.clusterDeps() == nil {
			httpJSONErr(w, http.StatusNotFound, "this daemon is not a cluster hub")
			return
		}
		h(w, r)
	}
}

// clusterSensitivePaths are the control-plane GET paths that require a token.
// The registry exposes base URLs and the routing map exposes the whole
// topology; neither belongs to an arbitrary browser tab.
var clusterSensitivePaths = []string{"/instances", "/cluster"}

// instanceView is the API shape of one registered instance: its configuration
// joined with what the prober last observed.
type instanceView struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	BaseURL       string           `json:"base_url"`
	Enabled       bool             `json:"enabled"`
	Self          bool             `json:"self"`
	Labels        []string         `json:"labels"`
	TokenError    string           `json:"token_error,omitempty"`
	AssignedRepos int              `json:"assigned_repos"`
	IsFallback    bool             `json:"is_fallback"`
	InPool        bool             `json:"in_pool"`
	State         *instances.State `json:"state,omitempty"`
}

func (srv *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	deps := srv.clusterDeps()

	assigned := map[string]int{}
	rules := snap.Router.RulesSnapshot()
	for _, id := range rules.Repos {
		assigned[id]++
	}
	pool := snap.Router.Pool()
	fallback := snap.Router.Fallback()

	views := make([]instanceView, 0, snap.Registry.Len())
	for _, inst := range snap.Registry.List() {
		v := instanceView{
			ID: inst.ID, Name: inst.Name, BaseURL: inst.BaseURL,
			Enabled: inst.Enabled, Self: inst.Self,
			Labels:        inst.Labels,
			AssignedRepos: assigned[inst.ID],
			IsFallback:    inst.ID == fallback,
			InPool:        containsString(pool, inst.ID),
		}
		if v.Labels == nil {
			v.Labels = []string{}
		}
		if inst.TokenErr != nil {
			v.TokenError = inst.TokenErr.Error()
		}
		if deps != nil && deps.Prober != nil {
			st := deps.Prober.State(inst.ID)
			v.State = &st
		}
		views = append(views, v)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"role":      snap.Role,
		"self_id":   snap.SelfID,
		"self_name": snap.SelfName,
		"instances": views,
	})
}

// registerInstanceRequest is the POST /instances body.
type registerInstanceRequest struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	BaseURL   string   `json:"base_url"`
	Token     string   `json:"token"`
	TokenEnv  string   `json:"token_env"`
	TokenFile string   `json:"token_file"`
	Labels    []string `json:"labels"`
	// SkipProbe registers without contacting the instance first. Useful when
	// adding a machine that is not up yet; the default is to verify.
	SkipProbe bool `json:"skip_probe"`
}

func (srv *Server) handleRegisterInstance(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	if srv.configPath == "" {
		httpJSONErr(w, http.StatusServiceUnavailable, "registering instances requires a writable config path")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req registerInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpJSONErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if err := config.ValidateInstanceBaseURL(req.BaseURL); err != nil {
		httpJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}

	sources := 0
	for _, set := range []bool{req.Token != "", req.TokenEnv != "", req.TokenFile != ""} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		httpJSONErr(w, http.StatusBadRequest, "exactly one of token, token_env or token_file is required")
		return
	}

	// Probe before writing. Registering an instance that answers is worth the
	// extra round trip: the alternative is a registry entry that looks fine and
	// silently never works, which is much harder to diagnose later.
	var health instances.Health
	if !req.SkipProbe {
		probe := instances.Instance{
			ID: "candidate", Name: req.Name, BaseURL: req.BaseURL, Token: req.Token, Enabled: true,
		}
		deps := srv.clusterDeps()
		h, err := deps.clientFor(probe).Health(r.Context())
		if err != nil {
			httpJSONErr(w, http.StatusBadGateway,
				fmt.Sprintf("could not reach the instance at %s: %v", req.BaseURL, err))
			return
		}
		health = h
	}

	// Prefer the id the instance reports for itself: it is the identity it will
	// use in its own logs and health responses, so adopting it keeps the two
	// sides talking about the same name.
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = health.InstanceID
	}
	if id == "" {
		id = slugFromName(req.Name, req.BaseURL)
	}
	if err := config.ValidateInstanceID(id); err != nil {
		httpJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, exists := snap.Registry.Get(id); exists {
		httpJSONErr(w, http.StatusConflict, fmt.Sprintf("instance %q is already registered", id))
		return
	}
	if req.Name == "" {
		req.Name = instances.Sanitize(health.InstanceName)
	}
	if req.Name == "" {
		req.Name = id
	}

	entry := map[string]any{"name": req.Name, "base_url": req.BaseURL}
	switch {
	case req.Token != "":
		entry["token"] = req.Token
	case req.TokenEnv != "":
		entry["token_env"] = req.TokenEnv
	default:
		entry["token_file"] = req.TokenFile
	}
	if len(req.Labels) > 0 {
		entry["labels"] = toAnySlice(req.Labels)
	}

	result, err := srv.patchClusterTOML(func(cluster map[string]any) error {
		insts := childTable(cluster, "instances")
		insts[id] = entry
		return nil
	})
	if err != nil {
		srv.writeClusterErr(w, "register instance", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "config": result})
}

// patchInstanceRequest is the PATCH /instances/{id} body. Pointer fields
// distinguish "not supplied" from "set to the zero value".
type patchInstanceRequest struct {
	Name      *string   `json:"name"`
	BaseURL   *string   `json:"base_url"`
	Token     *string   `json:"token"`
	TokenEnv  *string   `json:"token_env"`
	TokenFile *string   `json:"token_file"`
	Enabled   *bool     `json:"enabled"`
	Labels    *[]string `json:"labels"`
}

func (srv *Server) handlePatchInstance(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	id := chi.URLParam(r, "id")
	if _, exists := snap.Registry.Get(id); !exists {
		httpJSONErr(w, http.StatusNotFound, fmt.Sprintf("unknown instance %q", id))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req patchInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpJSONErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.BaseURL != nil {
		trimmed := strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/")
		if err := config.ValidateInstanceBaseURL(trimmed); err != nil {
			httpJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.BaseURL = &trimmed
	}

	result, err := srv.patchClusterTOML(func(cluster map[string]any) error {
		insts := childTable(cluster, "instances")
		entry := childTable(insts, id)
		if req.Name != nil {
			entry["name"] = strings.TrimSpace(*req.Name)
		}
		if req.BaseURL != nil {
			entry["base_url"] = *req.BaseURL
		}
		if req.Enabled != nil {
			entry["enabled"] = *req.Enabled
		}
		if req.Labels != nil {
			entry["labels"] = toAnySlice(*req.Labels)
		}
		// Rotating to a different token source must clear the previous one, or
		// the config would declare two and fail validation on the next load.
		if req.Token != nil || req.TokenEnv != nil || req.TokenFile != nil {
			delete(entry, "token")
			delete(entry, "token_env")
			delete(entry, "token_file")
			switch {
			case req.Token != nil && *req.Token != "":
				entry["token"] = *req.Token
			case req.TokenEnv != nil && *req.TokenEnv != "":
				entry["token_env"] = *req.TokenEnv
			case req.TokenFile != nil && *req.TokenFile != "":
				entry["token_file"] = *req.TokenFile
			default:
				return &config.ValidationError{Err: errors.New("an instance needs one of token, token_env or token_file")}
			}
		}
		return nil
	})
	if err != nil {
		srv.writeClusterErr(w, "patch instance", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	id := chi.URLParam(r, "id")
	if _, exists := snap.Registry.Get(id); !exists {
		httpJSONErr(w, http.StatusNotFound, fmt.Sprintf("unknown instance %q", id))
		return
	}
	if id == snap.SelfID {
		httpJSONErr(w, http.StatusConflict, "the hub cannot deregister itself")
		return
	}

	result, err := srv.patchClusterTOML(func(cluster map[string]any) error {
		delete(childTable(cluster, "instances"), id)
		// Leaving rules pointing at a removed instance would fail validation on
		// the very next load, so the removal has to take its references with it.
		routing := childTable(cluster, "routing")
		for _, scope := range []string{"orgs", "repos"} {
			table := childTable(routing, scope)
			for key, target := range table {
				if s, ok := target.(string); ok && s == id {
					delete(table, key)
				}
			}
		}
		if pool, ok := routing["round_robin_pool"].([]any); ok {
			kept := make([]any, 0, len(pool))
			for _, entry := range pool {
				if s, ok := entry.(string); !ok || s != id {
					kept = append(kept, entry)
				}
			}
			routing["round_robin_pool"] = kept
		}
		if def, ok := cluster["default_instance"].(string); ok && def == id {
			delete(cluster, "default_instance")
		}
		return nil
	})
	if err != nil {
		srv.writeClusterErr(w, "delete instance", err)
		return
	}
	// A stale observed-state row would keep the instance visible in the UI
	// after it was removed from the registry.
	if deps := srv.clusterDeps(); deps != nil && deps.Store != nil {
		if err := deps.Store.DeleteInstanceState(id); err != nil {
			slog.Warn("cluster: could not delete instance state", "instance", id, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handleProbeInstance(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	deps := srv.clusterDeps()
	if deps.Prober == nil {
		httpJSONErr(w, http.StatusServiceUnavailable, "health probing is not enabled")
		return
	}
	id := chi.URLParam(r, "id")
	inst, err := snap.Registry.Require(id)
	if err != nil {
		httpJSONErr(w, http.StatusNotFound, fmt.Sprintf("unknown instance %q", id))
		return
	}
	writeJSON(w, http.StatusOK, deps.Prober.Probe(r.Context(), inst))
}

// routingView is the API shape of the routing rules.
type routingView struct {
	Mode            string            `json:"mode"`
	RoundRobinPool  []string          `json:"round_robin_pool"`
	RoundRobinOps   []string          `json:"round_robin_ops"`
	Orgs            map[string]string `json:"orgs"`
	Repos           map[string]string `json:"repos"`
	DefaultInstance string            `json:"default_instance"`
	ResolvedPool    []string          `json:"resolved_pool"`
	Enabled         bool              `json:"enabled"`
}

func (srv *Server) handleGetRouting(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	rules := snap.Router.RulesSnapshot()
	writeJSON(w, http.StatusOK, routingView{
		Mode:            orDefault(rules.Mode, config.ModeAssignment),
		RoundRobinPool:  nonNilStrings(rules.RoundRobinPool),
		RoundRobinOps:   nonNilStrings(rules.RoundRobinOps),
		Orgs:            nonNilMap(rules.Orgs),
		Repos:           nonNilMap(rules.Repos),
		DefaultInstance: snap.Router.Fallback(),
		ResolvedPool:    nonNilStrings(snap.Router.Pool()),
		Enabled:         snap.Router.Enabled(),
	})
}

// putRoutingRequest is the PUT /cluster/routing body. Nil fields are left
// untouched so a client can change the mode without resending the whole map.
type putRoutingRequest struct {
	Mode            *string            `json:"mode"`
	RoundRobinPool  *[]string          `json:"round_robin_pool"`
	RoundRobinOps   *[]string          `json:"round_robin_ops"`
	Orgs            *map[string]string `json:"orgs"`
	Repos           *map[string]string `json:"repos"`
	DefaultInstance *string            `json:"default_instance"`
}

func (srv *Server) handlePutRouting(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req putRoutingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpJSONErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate every referenced instance up front so a typo is a 400 naming the
	// bad id, not a 500 from the config validator after the file was rewritten.
	known := func(id string) error {
		if _, exists := snap.Registry.Get(id); !exists {
			return fmt.Errorf("unknown instance %q", id)
		}
		return nil
	}
	if req.Orgs != nil {
		for org, id := range *req.Orgs {
			if err := config.ValidateOrgSlug(org); err != nil {
				httpJSONErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := known(id); err != nil {
				httpJSONErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}
	if req.Repos != nil {
		for repo, id := range *req.Repos {
			if err := config.ValidateRepoSlug(repo); err != nil {
				httpJSONErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := known(id); err != nil {
				httpJSONErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}
	if req.RoundRobinPool != nil {
		for _, id := range *req.RoundRobinPool {
			if err := known(id); err != nil {
				httpJSONErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}
	if req.DefaultInstance != nil && *req.DefaultInstance != "" {
		if err := known(*req.DefaultInstance); err != nil {
			httpJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	result, err := srv.patchClusterTOML(func(cluster map[string]any) error {
		routing := childTable(cluster, "routing")
		if req.Mode != nil {
			routing["mode"] = *req.Mode
		}
		if req.RoundRobinPool != nil {
			routing["round_robin_pool"] = toAnySlice(*req.RoundRobinPool)
		}
		if req.RoundRobinOps != nil {
			routing["round_robin_ops"] = toAnySlice(*req.RoundRobinOps)
		}
		// Maps are replaced wholesale rather than merged: PUT semantics, and
		// merging would make deleting a rule impossible.
		if req.Orgs != nil {
			routing["orgs"] = toAnyMap(*req.Orgs)
		}
		if req.Repos != nil {
			routing["repos"] = toAnyMap(*req.Repos)
		}
		if req.DefaultInstance != nil {
			if *req.DefaultInstance == "" {
				delete(cluster, "default_instance")
			} else {
				cluster["default_instance"] = *req.DefaultInstance
			}
		}
		return nil
	})
	if err != nil {
		srv.writeClusterErr(w, "update routing", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// propagateRequest is the POST /cluster/propagate body.
type propagateRequest struct {
	// Targets restricts the push. Empty means every registered instance.
	Targets []string `json:"targets"`
	// Patch is what to send. Empty means "this hub's current config", which is
	// what the GUI's "apply to all" button uses.
	Patch map[string]any `json:"patch"`
}

func (srv *Server) handlePropagateConfig(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req propagateRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httpJSONErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	patch := req.Patch
	if len(patch) == 0 {
		source, err := srv.readConfigTOML()
		if err != nil {
			srv.writeClusterErr(w, "read config for propagation", err)
			return
		}
		patch = source
	}
	if err := config.ContainsNull(patch); err != nil {
		httpJSONErr(w, http.StatusBadRequest, "null values not allowed — use DELETE to remove fields")
		return
	}
	config.NormalizeNumbers(patch)

	if len(req.Targets) > 0 {
		anyKnown := false
		for _, id := range req.Targets {
			if _, exists := snap.Registry.Get(id); exists {
				anyKnown = true
				break
			}
		}
		if !anyKnown {
			httpJSONErr(w, http.StatusBadRequest, instances.ErrNoTargets(req.Targets).Error())
			return
		}
	}

	filtered, dropped := instances.FilterPropagatable(patch)
	results := snap.Propagator.Propagate(r.Context(), filtered, req.Targets)

	failures := 0
	for _, res := range results {
		if !res.OK {
			failures++
		}
	}
	// 207 tells the GUI "read the per-instance results" without pretending a
	// partial push fully succeeded. A total failure is still a 207: the caller
	// needs the per-instance reasons either way.
	status := http.StatusOK
	if failures > 0 {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, map[string]any{
		"results":       results,
		"skipped_local": dropped,
		"failures":      failures,
	})
}

func (srv *Server) handleConfigDrift(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	if srv.configFn == nil {
		httpJSONErr(w, http.StatusServiceUnavailable, "config snapshot not available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instances": snap.Propagator.DetectDrift(r.Context(), srv.configFn(), nil),
	})
}

// dispatchRequest is the POST /cluster/dispatch/{op} body.
type dispatchRequest struct {
	PRID    int64  `json:"pr_id"`
	IssueID int64  `json:"issue_id"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	HeadSHA string `json:"head_sha"`
	PRURL   string `json:"pr_url"`
	DryRun  bool   `json:"dry_run"`
	// Instance forces a target, bypassing routing. Used by the GUI's
	// "run this here" action.
	Instance string `json:"instance"`
}

func (srv *Server) handleDispatch(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	op := strings.ToLower(chi.URLParam(r, "op"))
	switch op {
	case config.OpReview, config.OpMerge, config.OpIssue:
	default:
		httpJSONErr(w, http.StatusBadRequest,
			fmt.Sprintf("unknown operation %q; expected review, merge or issue", op))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req dispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpJSONErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Repo != "" {
		if err := config.ValidateRepoSlug(req.Repo); err != nil {
			httpJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	target, err := srv.resolveDispatchTarget(snap, op, req)
	if err != nil {
		httpJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	inst, err := snap.Registry.Require(target)
	if err != nil {
		httpJSONErr(w, http.StatusNotFound, err.Error())
		return
	}

	// Deduplicate: a retry, or two GUI clients clicking at once, must not send
	// the same work twice. A new head SHA is genuinely a new operation.
	key := dispatchKey(op, req)
	deps := srv.clusterDeps()
	claimed := false
	if deps != nil && deps.Store != nil {
		ok, err := deps.Store.ClaimDispatch(op, key, req.HeadSHA, target)
		if err != nil {
			slog.Error("cluster: dispatch claim failed", "op", op, "key", key, "err", err)
			httpJSONErr(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if !ok {
			existing, _, _ := deps.Store.DispatchTarget(op, key, req.HeadSHA)
			writeJSON(w, http.StatusOK, map[string]any{
				"instance_id": existing,
				"duplicate":   true,
				"detail":      "this operation was already dispatched for this commit",
			})
			return
		}
		claimed = true
	}

	if err := srv.executeDispatch(r.Context(), op, inst, req); err != nil {
		slog.Error("cluster: dispatch failed", "op", op, "instance", target, "err", err)
		// Release the claim: the work did not happen, and keeping it would make
		// a transient failure block this operation for this commit forever,
		// with the retry answered as a duplicate of something that never ran.
		if claimed {
			if relErr := deps.Store.ReleaseDispatch(op, key, req.HeadSHA); relErr != nil {
				slog.Warn("cluster: could not release a failed dispatch claim",
					"op", op, "key", key, "err", relErr)
			}
		}
		httpJSONErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"instance_id":   target,
		"instance_name": inst.Name,
		"op":            op,
	})
}

// resolveDispatchTarget picks the instance for an operation: an explicit
// override first, then per-operation round robin when dispatch mode is on, then
// the repo's owner.
func (srv *Server) resolveDispatchTarget(snap ClusterSnapshot, op string, req dispatchRequest) (string, error) {
	if req.Instance != "" {
		return req.Instance, nil
	}
	if snap.Router.RoundRobinsOp(op) {
		var healthy []string
		if deps := srv.clusterDeps(); deps != nil && deps.Prober != nil {
			healthy = deps.Prober.HealthyIDs()
		}
		if picked := snap.Router.NextAmong(op, healthy); picked != "" {
			return picked, nil
		}
	}
	if req.Repo != "" {
		if owner := snap.Router.OwnerFor(req.Repo); owner != "" {
			return owner, nil
		}
	}
	if fallback := snap.Router.Fallback(); fallback != "" {
		return fallback, nil
	}
	if snap.SelfID != "" {
		return snap.SelfID, nil
	}
	return "", errors.New("no instance available to take this operation")
}

// executeDispatch performs the operation on the chosen instance. When the hub
// itself is the target it calls its own in-process trigger rather than looping
// back over HTTP.
func (srv *Server) executeDispatch(ctx context.Context, op string, inst instances.Instance, req dispatchRequest) error {
	if inst.Self {
		switch op {
		case config.OpReview:
			if srv.triggerReviewFn == nil {
				return errors.New("this daemon cannot trigger reviews")
			}
			return srv.triggerReviewFn(req.PRID)
		case config.OpIssue:
			if srv.triggerIssueReviewFn == nil {
				return errors.New("this daemon cannot trigger issue reviews")
			}
			return srv.triggerIssueReviewFn(req.IssueID)
		case config.OpMerge:
			if srv.mergeTrackEvaluateFn == nil {
				return errors.New("this daemon cannot evaluate merge tracking")
			}
			return srv.mergeTrackEvaluateFn(ctx, req.PRID, req.DryRun)
		}
		return fmt.Errorf("unsupported operation %q", op)
	}

	client := srv.clusterDeps().clientFor(inst)
	switch op {
	case config.OpReview:
		// An instance that does not own the repo has never seen the PR, so it
		// has to adopt it before it can review it. Ignoring an add failure is
		// deliberate: the PR may already be known, in which case the review
		// call below is the one whose result matters.
		if req.PRURL != "" {
			if _, err := client.AddPR(ctx, req.PRURL); err != nil {
				slog.Debug("cluster: add-PR before dispatched review failed",
					"instance", inst.ID, "err", err)
			}
		}
		return client.TriggerPRReview(ctx, req.PRID)
	case config.OpIssue:
		return client.TriggerIssueReview(ctx, req.IssueID)
	case config.OpMerge:
		return client.EvaluateMergeTracking(ctx, req.PRID, req.DryRun)
	}
	return fmt.Errorf("unsupported operation %q", op)
}

func dispatchKey(op string, req dispatchRequest) string {
	switch op {
	case config.OpIssue:
		if req.Repo != "" && req.Number > 0 {
			return fmt.Sprintf("%s#%d", req.Repo, req.Number)
		}
		return strconv.FormatInt(req.IssueID, 10)
	default:
		if req.Repo != "" && req.Number > 0 {
			return fmt.Sprintf("%s#%d", req.Repo, req.Number)
		}
		return strconv.FormatInt(req.PRID, 10)
	}
}

// proxyAllowedPrefixes are the paths the hub will forward to an instance.
//
// An allowlist rather than a denylist: the proxy exists so the GUI can read one
// instance's data through the hub, and anything outside that is a capability
// nobody asked for. Notably absent are /shutdown (a remote daemon has no
// supervisor to bring it back — unlike the local one the desktop app can
// respawn), /update/* (the replacement handshake is bound to a single process)
// and /admin/* and /instances (no nested proxying).
var proxyAllowedPrefixes = []string{
	"/health", "/me", "/prs", "/issues", "/activity", "/stats",
	"/github/rate_limit", "/agents", "/config", "/merge-tracking",
	"/repos", "/events", "/logs/stream", "/reload",
}

func proxyPathAllowed(path string) bool {
	for _, prefix := range proxyAllowedPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") || strings.HasPrefix(path, prefix+"?") {
			return true
		}
	}
	return false
}

// handleInstanceProxy forwards a request to a registered instance.
//
// This is the GUI's transport for every instance but the hub's own. It exists
// so the browser build keeps talking to exactly one origin with one token: the
// alternative is CORS on every daemon plus every instance's token shipped to
// the front end, which is both more work and strictly worse for security.
//
// The target is always looked up in the registry by id — never taken from the
// request — so there is no URL a client can supply that makes the hub issue an
// arbitrary outbound request.
func (srv *Server) handleInstanceProxy(w http.ResponseWriter, r *http.Request) {
	snap, ok := srv.snapshot()
	if !ok {
		httpJSONErr(w, http.StatusServiceUnavailable, "cluster control plane not available")
		return
	}
	id := chi.URLParam(r, "id")
	inst, err := snap.Registry.Require(id)
	if err != nil {
		httpJSONErr(w, http.StatusNotFound, fmt.Sprintf("unknown instance %q", id))
		return
	}

	rest := "/" + strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if !proxyPathAllowed(rest) {
		httpJSONErr(w, http.StatusForbidden, fmt.Sprintf("path %q is not proxyable", rest))
		return
	}

	// The hub serves its own data directly. Going out over the network to
	// ourselves would work but turns a loopback hiccup into an instance that
	// looks down while it is demonstrably answering this very request.
	if inst.Self {
		clone := r.Clone(r.Context())
		clone.URL.Path = rest
		clone.RequestURI = ""
		srv.router.ServeHTTP(w, clone)
		return
	}

	if !inst.Enabled {
		httpJSONErr(w, http.StatusConflict, fmt.Sprintf("instance %q is disabled", id))
		return
	}
	if inst.TokenErr != nil {
		httpJSONErr(w, http.StatusBadGateway,
			fmt.Sprintf("instance %q has no usable token: %v", id, inst.TokenErr))
		return
	}

	target, err := url.Parse(inst.BaseURL)
	if err != nil {
		httpJSONErr(w, http.StatusInternalServerError, "instance base_url is unusable")
		return
	}

	deps := srv.clusterDeps()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = strings.TrimRight(target.Path, "/") + rest
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = target.Host
			// Never forward the caller's credentials: they authenticate to the
			// hub, and the instance has its own token.
			pr.Out.Header.Del(instances.HeaderToken)
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set(instances.HeaderToken, inst.Token)
			pr.SetXForwarded()
		},
		// -1 disables buffering so SSE (/events, /logs/stream) streams through
		// instead of arriving in one lump when the connection finally closes.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn("cluster: proxy to instance failed", "instance", id, "path", rest, "err", err)
			httpJSONErr(w, http.StatusBadGateway,
				fmt.Sprintf("instance %q is unreachable: %v", id, err))
		},
	}
	if deps != nil && deps.HTTPClient != nil {
		proxy.Transport = deps.HTTPClient.Transport
	}
	proxy.ServeHTTP(w, r)
}

// patchClusterTOML applies a mutation scoped to the [cluster] table, then
// validates, writes and reloads through the existing TOML machinery — so a
// rejected edit leaves config.toml exactly as it was.
func (srv *Server) patchClusterTOML(mutate func(cluster map[string]any) error) (map[string]any, error) {
	if srv.configPath == "" {
		return nil, errors.New("configPath not set")
	}
	selfID, _, _ := srv.clusterIdentity()
	return srv.patchTOML(func(m map[string]any) error {
		cluster := childTable(m, "cluster")
		// The runtime derives this daemon's id from <dataDir>/instance_id and
		// seeds its own registry entry in memory (ensureSelfInstance), so
		// neither is visible to config.ValidateMap, which only ever sees the
		// file. Sealing the id into config.toml the first time the operator
		// edits cluster config lets a rule that names this daemon — "owner of
		// everything unrouted", an org, a repo — pass here and on the next
		// boot, instead of being rejected as an unknown instance.
		if selfID != "" {
			if cur, _ := cluster["instance_id"].(string); strings.TrimSpace(cur) == "" {
				cluster["instance_id"] = selfID
			}
		}
		return mutate(cluster)
	})
}

// readConfigTOML returns the hub's config.toml as a map, for propagation.
func (srv *Server) readConfigTOML() (map[string]any, error) {
	if srv.configPath == "" {
		return nil, errors.New("configPath not set")
	}
	srv.tomlMu.Lock()
	defer srv.tomlMu.Unlock()
	return config.ReadTOMLMap(srv.configPath)
}

func (srv *Server) writeClusterErr(w http.ResponseWriter, what string, err error) {
	slog.Error("cluster: "+what+" failed", "err", err)
	var ve *config.ValidationError
	if errors.As(err, &ve) {
		httpJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSONErr(w, http.StatusInternalServerError, "internal server error")
}

// childTable returns m[key] as a table, creating it when absent or when the
// existing value is not a table (a hand-edited scalar must not make every
// subsequent edit fail).
func childTable(m map[string]any, key string) map[string]any {
	if existing, ok := m[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	m[key] = created
	return created
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func toAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// slugFromName derives an instance id when neither the caller nor the instance
// supplied one.
func slugFromName(name, baseURL string) string {
	source := strings.TrimSpace(name)
	if source == "" {
		if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
			source = u.Host
		}
	}
	var b strings.Builder
	for _, r := range strings.ToLower(source) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.' || r == ':':
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if slug == "" || !(slug[0] >= 'a' && slug[0] <= 'z') && !(slug[0] >= '0' && slug[0] <= '9') {
		slug = "instance-" + strconv.FormatInt(time.Now().UnixNano()%1e6, 10)
	}
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	return slug
}
