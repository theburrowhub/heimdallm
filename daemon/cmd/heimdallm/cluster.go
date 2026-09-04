package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/instances"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// instanceIDFileMode matches the API token's: this file is not a secret, but it
// lives in the same directory and there is no reason for it to be broader.
const instanceIDFileMode = 0o600

// clusterState owns everything the multi-instance control plane needs at
// runtime. One is built at startup and updated in place on every config reload.
//
// The Router is deliberately long-lived and updated rather than rebuilt: its
// round-robin counters are the rotation itself, so replacing it would send every
// operation back to the first instance in the pool.
type clusterState struct {
	mu       sync.RWMutex
	registry *instances.Registry
	router   *instances.Router
	prober   *instances.Prober
	factory  instances.ClientFactory
	selfID   string
	selfName string
	role     string

	// takeoverAfterFailedProbes is how many consecutive failed probes the hub
	// requires before it stops deferring to a routed owner. See
	// config.ClusterConfig.TakeoverAfterFailedProbes.
	takeoverAfterFailedProbes int

	// LAN discovery. Both are nil unless cluster.discovery is on, and the
	// discoverer is nil unless this daemon is also the hub.
	//
	// Deliberately no sockets here: those belong to the loops in
	// discovery_lan.go. A socket owned by this struct would have to be closed
	// by whoever noticed the config changed, which on this daemon is the reload
	// goroutine — while the loop using it is still reading.
	discoverer   *instances.Discoverer
	discoverySig discoverySignature

	// served is the HTTP listener's real address, set once at startup. The
	// advertiser publishes this rather than server.port: the listener is bound
	// before any reload can touch it, so the configured port and the served
	// port can legitimately disagree.
	served *net.TCPAddr

	// store and broker are retained (rather than only used inline at
	// construction) so Update can lazily build a prober if this daemon is
	// promoted from worker/standalone to hub by a config reload instead of a
	// restart. May be nil in tests.
	store  instances.StateStore
	broker *sse.Broker

	// notes is what this daemon remembers about each routed instance between
	// poll cycles: which notices it already reported, and how many dispatches
	// to it have failed in a row. See instanceNotes for the locking rationale.
	notes instanceNotes
}

// ownerVerdict is what this daemon must do with one repo's autonomous work.
//
// The type exists because the previous boolean ("is the owner healthy?")
// collapsed two very different situations into one answer. An owner that has
// stopped working must be taken over or its repos go unreviewed; an owner that
// is merely unreachable *from here* is still polling GitHub and reviewing its
// own repos, so taking over publishes a second, independently-reasoned review
// on the same PR. See theburrowhub/heimdallm#765.
type ownerVerdict int

const (
	// verdictActLocally: nothing is routed away from this daemon — no owner is
	// configured, the owner is this daemon, or the owner was never registered
	// (a routing typo, where acting locally beats orphaning the repo).
	verdictActLocally ownerVerdict = iota
	// verdictDispatch: the owner is another instance we can currently reach.
	verdictDispatch
	// verdictDeferToOwner: the owner is another instance we cannot reach but
	// have not yet given up on. The work is left to it.
	verdictDeferToOwner
	// verdictTakeOver: the owner is another instance we have given up on after
	// takeoverAfterFailedProbes consecutive failed probes. This daemon does its
	// work, and says so loudly, because it may be duplicating it.
	verdictTakeOver
	// verdictNotAssigned: this daemon is a worker (cluster.role = "worker"),
	// and repo resolves to no owner it can confirm is itself — no rule, no
	// registered owner, or an owner it cannot use. Unlike verdictActLocally on
	// a hub or standalone daemon, this must NOT be read as "acting locally is
	// safe": a worker has no registry of its own to fall back on, the hub is
	// the only authority on the partition, and reviewing everything it
	// discovers while waiting for that partition is exactly the bug behind
	// theburrowhub/heimdallm#769. The repo is left alone until the hub assigns
	// it explicitly (PUT /cluster/partition). See Router.WithheldFromWorker.
	verdictNotAssigned
)

// instanceNotes is the hub's short-term memory about each routed instance: the
// notices already reported for it, and how many times a dispatch to it has
// failed in a row.
//
// Both are keyed by instance first so one call wipes everything about an
// instance that recovered or was removed from the config — the same pruning
// Prober.Update does for its own states.
//
// Its own mutex rather than cs.mu: every caller here also reads cs.mu (through
// probeFailures / takeoverThreshold), and one lock covering both would make
// every future caller reason about ordering. Nothing in this type touches
// cs.mu, so there is no ordering to get wrong.
type instanceNotes struct {
	mu        sync.Mutex
	announced map[string]map[string]bool // instance id -> subject -> reported
	failures  map[string]map[string]int  // instance id -> work unit -> consecutive failures
}

// claim reports whether this is the first notice for (instanceID, subject).
// Build the subject with noticeSubject so clearUnit can find it again.
func (n *instanceNotes) claim(instanceID, subject string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.announced == nil {
		n.announced = map[string]map[string]bool{}
	}
	subjects := n.announced[instanceID]
	if subjects == nil {
		subjects = map[string]bool{}
		n.announced[instanceID] = subjects
	}
	if subjects[subject] {
		return false
	}
	subjects[subject] = true
	return true
}

// recordFailure increments the consecutive-failure count for one work unit and
// returns the new total.
func (n *instanceNotes) recordFailure(instanceID, unit string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failures == nil {
		n.failures = map[string]map[string]int{}
	}
	units := n.failures[instanceID]
	if units == nil {
		units = map[string]int{}
		n.failures[instanceID] = units
	}
	units[unit]++
	return units[unit]
}

// clearUnit forgets everything recorded about one work unit: its consecutive
// failure count and every notice reported about it.
//
// Called when a dispatch succeeds, and that is the point. The count has to be
// *consecutive* failures — a repo that fails once a day is a blip, not an owner
// refusing work. But clearing the count alone left the notices claimed, and
// nothing else could clear them in the case that matters most: an owner that
// rejects authenticated dispatches while still answering the unauthenticated
// health probe never produces a reachability transition, so forget() never
// runs. The second time that happened — token rotated again weeks later — the
// hub took over in complete silence, which is the failure #765 was about.
//
// A successful dispatch is as much a recovery signal as a successful probe, so
// it clears both.
func (n *instanceNotes) clearUnit(instanceID, unit string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if units := n.failures[instanceID]; units != nil {
		delete(units, unit)
		if len(units) == 0 {
			delete(n.failures, instanceID)
		}
	}
	subjects := n.announced[instanceID]
	if subjects == nil {
		return
	}
	suffix := "#" + unit
	for subject := range subjects {
		if strings.HasSuffix(subject, suffix) {
			delete(subjects, subject)
		}
	}
	if len(subjects) == 0 {
		delete(n.announced, instanceID)
	}
}

// forget drops every note about instanceID, so a later outage is reported as a
// new event instead of being swallowed as a repeat of the last one.
func (n *instanceNotes) forget(instanceID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.announced, instanceID)
	delete(n.failures, instanceID)
}

// retain drops notes about instances that are no longer registered. Without it
// a config reload that removes an instance leaks its entries for the lifetime
// of the process.
func (n *instanceNotes) retain(registered map[string]bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id := range n.announced {
		if !registered[id] {
			delete(n.announced, id)
		}
	}
	for id := range n.failures {
		if !registered[id] {
			delete(n.failures, id)
		}
	}
}

// takeoverReason says why this daemon stopped deferring to a routed owner.
// Each has a different cause and a different thing for the operator to check,
// so they must not share one message.
type takeoverReason int

const (
	// takeoverProbesFailed: the owner stopped answering health probes. Usually
	// a dead machine — or a stale base_url, which looks identical from here.
	takeoverProbesFailed takeoverReason = iota
	// takeoverDispatchRejected: the owner answers /health but keeps rejecting
	// our authenticated calls. It is running, and it is refusing this work.
	takeoverDispatchRejected
	// takeoverNoObserver: this daemon is not a hub, so it has no health
	// history for anyone and cannot claim the owner is fine. Handling the work
	// locally on a failed RPC is what it always did.
	takeoverNoObserver
)

// label is the reason's stable name, used both in the dedup subject and in the
// SSE payload so the two can never disagree about which condition fired.
//
// It is part of the subject, not just the message: a work unit can hit one
// reason and later the other (rejected dispatches first, then the instance
// stops answering probes at all), and those need different fixes. A
// reason-agnostic key reported only whichever came first, which would have
// made the per-reason messages below pointless.
func (r takeoverReason) label() string {
	switch r {
	case takeoverDispatchRejected:
		return "dispatch_rejected"
	case takeoverNoObserver:
		return "no_observer"
	default:
		return "probes_failed"
	}
}

// dispatchUnit names one unit of routed work: one operation on one repo. It is
// the granularity both the failure count and the notices use, because "this
// remote does not have this repo configured" is specific to one repo and one
// operation, not to the instance.
func dispatchUnit(op, repo string) string { return op + "|" + repo }

// noticeSubject builds the dedup key for one notice about one work unit.
//
// The unit always comes last, after "#", so clearUnit can drop every notice
// about a unit without having to know which notices exist.
func noticeSubject(notice, unit string) string { return notice + "#" + unit }

// ensureSelfInstance gives a hub an entry for itself when the operator has not
// written one.
//
// A hub is an instance like any other: the UI lists it, repos route to it, and
// its data is read through the same path. Requiring the operator to describe
// their own machine in config.toml would also be a chicken-and-egg trap —
// registering the first remote instance would fail validation because the hub
// had not registered itself yet.
//
// The entry is in-memory only. It is deliberately not written to config.toml:
// the file stays minimal, and an operator who does write an explicit entry
// keeps full control (theirs wins).
//
// base_url is loopback because nothing ever dials it: the proxy short-circuits
// the hub's own id and serves locally.
func ensureSelfInstance(cfg *config.Config, dataDir string) {
	id := cfg.Cluster.InstanceID
	if !cfg.IsHub() || id == "" {
		return
	}
	if _, exists := cfg.Cluster.Instances[id]; exists {
		return
	}
	if cfg.Cluster.Instances == nil {
		cfg.Cluster.Instances = map[string]config.InstanceConfig{}
	}
	port := cfg.Server.Port
	if port == 0 {
		port = 7842
	}
	cfg.Cluster.Instances[id] = config.InstanceConfig{
		Name:      resolvedSelfName(cfg),
		BaseURL:   fmt.Sprintf("http://127.0.0.1:%d", port),
		TokenFile: filepath.Join(dataDir, "api_token"),
	}
}

// newClusterState builds the control plane from the first loaded config.
func newClusterState(cfg *config.Config, st *store.Store, broker *sse.Broker) *clusterState {
	registry := instances.NewRegistry(cfg)
	cs := &clusterState{
		registry:                  registry,
		router:                    instances.NewRouter(registry, cfg),
		selfID:                    cfg.Cluster.InstanceID,
		selfName:                  resolvedSelfName(cfg),
		role:                      cfg.Cluster.Role,
		takeoverAfterFailedProbes: clusterTakeoverThreshold(cfg),
		broker:                    broker,
	}
	if st != nil {
		cs.store = st
	}
	cs.factory = func(inst instances.Instance) *instances.Client {
		return instances.NewClient(inst, nil)
	}
	// Only a hub probes. A worker knowing about its peers would be duplicated
	// effort and a second source of truth for the same question.
	if cfg.IsHub() {
		cs.prober = instances.NewProber(
			registry, clusterProbeInterval(cfg), cs.factory, cs.store, newClusterEvents(broker, cs.notes.forget),
		)
		cs.prober.SetSelfInfo(version, cfg.Cluster.Role)
	}
	cs.applyDiscovery(cfg, registry)
	return cs
}

// applyDiscovery records what discovery should be doing for cfg. Caller must
// not hold cs.mu.
//
// It only ever touches state, never a socket. Nothing needs to be told to start
// or stop a loop either: any change to [cluster] or server.port already forces
// a full poller restart (see configReloadRestartSnapshot), and both discovery
// loops live on the poller context — so they are torn down, close their own
// sockets on the way out, and start again reading whatever this recorded.
func (cs *clusterState) applyDiscovery(cfg *config.Config, registry *instances.Registry) {
	sig := newDiscoverySignature(cfg)

	cs.mu.Lock()
	cs.discoverySig = sig
	switch {
	case !sig.enabled || !sig.hub:
		cs.discoverer = nil
	case cs.discoverer == nil:
		// Built without a browser. RunDiscoverer lends it one for the lifetime
		// of the loop, so the cached view outlives a poller restart while the
		// socket does not.
		cs.discoverer = instances.NewDiscoverer(
			registry, nil, instances.DefaultDiscoveryInterval, cs.factory)
	}
	discoverer := cs.discoverer
	cs.mu.Unlock()

	if discoverer != nil {
		discoverer.Update(registry, instances.DefaultDiscoveryInterval)
	}
}

// Update swaps in a reloaded config. Called from the same reload path that
// restarts the pollers, so ownership and routing take effect on the next tick.
//
// Reports proberBuilt=true when this call just promoted the daemon from
// worker/standalone to hub and therefore built a prober that did not exist at
// boot. The caller must then start a fresh RunProber goroutine itself — this
// method only builds the prober, it never runs its loop, so a promotion that
// happens purely via reload (no process restart) still gets health probing
// and the /instances route wired without the operator having to restart.
func (cs *clusterState) Update(cfg *config.Config) (proberBuilt bool) {
	registry := instances.NewRegistry(cfg)

	cs.mu.Lock()
	cs.registry = registry
	cs.selfID = cfg.Cluster.InstanceID
	cs.selfName = resolvedSelfName(cfg)
	cs.role = cfg.Cluster.Role
	cs.takeoverAfterFailedProbes = clusterTakeoverThreshold(cfg)
	router := cs.router
	if cs.prober == nil && cfg.IsHub() {
		cs.prober = instances.NewProber(
			registry, clusterProbeInterval(cfg), cs.factory, cs.store, newClusterEvents(cs.broker, cs.notes.forget),
		)
		cs.prober.SetSelfInfo(version, cfg.Cluster.Role)
		proberBuilt = true
	}
	prober := cs.prober
	cs.mu.Unlock()

	// Drop notes about instances this reload removed. Prober.Update prunes its
	// own states for the same reason; without this the notebook keeps entries
	// for instances that no longer exist for the life of the process.
	registered := make(map[string]bool, registry.Len())
	for _, inst := range registry.List() {
		registered[inst.ID] = true
	}
	cs.notes.retain(registered)

	router.Update(registry, cfg)
	// A prober built above this tick (proberBuilt) already has this exact
	// registry and interval from its NewProber call; updating it again would
	// be a harmless but pointless no-op.
	if prober != nil && !proberBuilt {
		prober.Update(registry, clusterProbeInterval(cfg))
	}
	cs.applyDiscovery(cfg, registry)
	return proberBuilt
}

// ownerVerdictFor resolves what this daemon must do with repo's autonomous
// work, and is the single place the "unreachable vs not working" distinction
// is made.
//
// The four outcomes are documented on ownerVerdict. Two of them are the old
// fail-open behaviour under new names: verdictActLocally covers no configured
// owner, an owner that resolves to this daemon (Owns(repo) would already be
// true) and an owner that was never registered. verdictDispatch is the happy
// path.
//
// The split that matters is between the other two. Before #765 an owner the
// prober could not reach was treated as not working, and this daemon did its
// work — which is correct for a machine that died and wrong for one that only
// lost inbound reachability, because that machine is still polling GitHub and
// reviewing the very repos it owns. Two daemons then reviewed the same PR and
// published two independently-reasoned verdicts, with nothing to dedupe them.
//
// So an unreachable owner now yields verdictDeferToOwner until it has failed
// takeoverAfterFailedProbes consecutive probes, and verdictTakeOver after
// that. Deferring is not a hope that the work happens: the owner's own poll
// loop covers its repos without any help from the hub, so dispatch only ever
// accelerated work that instance would have found on its own tick.
//
// Fail-open in one place still, for a hub or standalone daemon: a nil prober
// means this daemon is not a hub (only a hub builds one) and has nothing to
// observe the owner with, so it dispatches and lets the RPC answer the
// question.
//
// A worker inverts every one of the three verdictActLocally branches below
// into verdictNotAssigned instead, via Router.WithheldFromWorker. A worker
// has no registry of its own — the hub is the only authority on who owns
// what — so "no owner resolvable" or "the resolved owner is not registered
// here" means "the hub has not assigned this to me", not "acting locally is
// safe". See verdictNotAssigned's own doc for why this distinction exists.
func (cs *clusterState) ownerVerdictFor(repo string) (instances.Instance, ownerVerdict) {
	if cs == nil {
		return instances.Instance{}, verdictActLocally
	}
	cs.mu.RLock()
	router, registry, prober := cs.router, cs.registry, cs.prober
	threshold := cs.takeoverAfterFailedProbes
	cs.mu.RUnlock()
	if router == nil || registry == nil {
		return instances.Instance{}, verdictActLocally
	}

	owner := router.OwnerFor(repo)
	if owner == "" {
		if router.WithheldFromWorker(repo) {
			return instances.Instance{}, verdictNotAssigned
		}
		return instances.Instance{}, verdictActLocally
	}
	inst, found := registry.Get(owner)
	if !found {
		if router.WithheldFromWorker(repo) {
			return instances.Instance{}, verdictNotAssigned
		}
		return instances.Instance{}, verdictActLocally
	}
	if inst.Self {
		return instances.Instance{}, verdictActLocally
	}
	// A rule can name an instance the operator has disabled, or one whose
	// token no longer resolves. Such an instance is not running our work and
	// never will, so deferring to it would orphan the repo — which is the
	// failure mode dispatch-with-fallback exists to prevent. It is also why
	// this check cannot be left to the HealthyIDs scan below: an unusable
	// instance is absent from HealthyIDs *and* is not ConfirmedDown (the
	// prober has no state for it), which lands on verdictDeferToOwner.
	if !inst.Usable() {
		if router.WithheldFromWorker(repo) {
			return instances.Instance{}, verdictNotAssigned
		}
		return instances.Instance{}, verdictActLocally
	}
	if prober == nil {
		return inst, verdictDispatch
	}
	if prober.ConfirmedDown(owner, threshold) {
		return inst, verdictTakeOver
	}
	for _, id := range prober.HealthyIDs() {
		if id == owner {
			return inst, verdictDispatch
		}
	}
	return inst, verdictDeferToOwner
}

// dispatch sends a repo's work to the instance the router says owns it,
// calling do against a client for that instance.
//
// handledElsewhere=true means the caller must NOT act on repo itself, for
// either of two reasons: the owner accepted the call, or the owner is
// unreachable from here but not confirmed down, in which case its own poll
// loop is expected to cover the repo and doing the work here would duplicate
// it. Only verdictActLocally and verdictTakeOver return false, and those are
// exactly the cases where nobody else will act.
//
// Before dispatch existed, Owns()==false just meant the poller skipped the
// repo outright (main.go's tier2Adapter did a bare `continue`/`return 0, nil`),
// so a repo routed to an instance that never got the dispatch went completely
// unreviewed with nothing to show for it. That safety net is still here — it
// just no longer fires on the first missed health probe. See
// theburrowhub/heimdallm#765.
func (cs *clusterState) dispatch(ctx context.Context, repo, op string, do func(*instances.Client) error) (handledElsewhere bool) {
	inst, verdict := cs.ownerVerdictFor(repo)
	// Only read when the verdict is verdictTakeOver. Reaching that straight
	// out of ownerVerdictFor means the prober gave up on the owner.
	reason := takeoverProbesFailed
	if verdict == verdictDispatch {
		unit := dispatchUnit(op, repo)
		if err := do(cs.clientFactory()(inst)); err == nil {
			cs.notes.clearUnit(inst.ID, unit)
			return true
		} else {
			cs.noteDispatchFailure(repo, op, inst, err)
		}
		verdict, reason = cs.verdictAfterFailedDispatch(inst, op, repo)
	}

	switch verdict {
	case verdictTakeOver:
		cs.announceTakeover(repo, op, inst, reason)
		return false
	case verdictDeferToOwner:
		cs.noteDeferral(repo, op, inst)
		return true
	case verdictNotAssigned:
		cs.noteNotAssigned(repo, op)
		return true
	default: // verdictActLocally
		return false
	}
}

// verdictAfterFailedDispatch decides what a rejected dispatch RPC means.
//
// It is a separate question from "is the owner answering health probes", and
// conflating the two is how the first cut of this change broke the safety net:
//
//   - A daemon with no prober is not a hub (only a hub builds one), so it has
//     no health history for anyone and no grounds to assert the owner is fine.
//     Deferring there would skip the work forever with nothing able to escalate
//     it. Handling it locally on a failed RPC is exactly what it did before
//     #765, and what ownerVerdictFor's own comment promises.
//   - On a hub, the probe is the *unauthenticated* GET /health while every
//     dispatch RPC is authenticated. An owner that answers /health and rejects
//     our calls — cluster token rotated on the remote, the repo missing from
//     that remote's own config, a permission error — never accumulates a probe
//     failure, so it would stay "healthy" and its work would be done by nobody.
//     Counting consecutive rejections and escalating on the same threshold the
//     probes use bounds that to takeoverAfterFailedProbes poll cycles.
//
// One rejection still means defer: an auth blip or a restarting remote produces
// it, and the owner's own poll loop covers the repo in the meantime.
func (cs *clusterState) verdictAfterFailedDispatch(inst instances.Instance, op, repo string) (ownerVerdict, takeoverReason) {
	if !cs.hasProber() {
		return verdictTakeOver, takeoverNoObserver
	}
	if cs.confirmedDown(inst.ID) {
		return verdictTakeOver, takeoverProbesFailed
	}
	if n := cs.notes.recordFailure(inst.ID, dispatchUnit(op, repo)); n >= cs.takeoverThreshold() {
		return verdictTakeOver, takeoverDispatchRejected
	}
	return verdictDeferToOwner, takeoverProbesFailed
}

// noteDispatchFailure reports a rejected dispatch RPC once per (instance,
// operation, repo) and at Debug thereafter. Undeduped it produced one WARN per
// routed repo per poll cycle for the whole outage — the same flooding the
// notices are deduped to avoid.
func (cs *clusterState) noteDispatchFailure(repo, op string, inst instances.Instance, err error) {
	msg := "cluster: dispatch to owning instance failed"
	args := []any{"repo", repo, "operation", op, "instance", inst.ID, "err", err}
	if cs.notes.claim(inst.ID, noticeSubject("rpc", dispatchUnit(op, repo))) {
		slog.Warn(msg, args...)
		return
	}
	slog.Debug(msg, args...)
}

func (cs *clusterState) clientFactory() instances.ClientFactory {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.factory
}

func (cs *clusterState) hasProber() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.prober != nil
}

// confirmedDown reports whether the prober has given up on instanceID. A
// daemon with no prober (not a hub) never gives up on anyone.
func (cs *clusterState) confirmedDown(instanceID string) bool {
	cs.mu.RLock()
	prober, threshold := cs.prober, cs.takeoverAfterFailedProbes
	cs.mu.RUnlock()
	return prober != nil && prober.ConfirmedDown(instanceID, threshold)
}

func (cs *clusterState) takeoverThreshold() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.takeoverAfterFailedProbes
}

func (cs *clusterState) probeFailures(instanceID string) int {
	cs.mu.RLock()
	prober := cs.prober
	cs.mu.RUnlock()
	if prober == nil {
		return 0
	}
	return prober.State(instanceID).ConsecutiveFailures
}

// noteDeferral records that this daemon left an unreachable owner's work
// alone. Reported once per (instance, operation, repo) outage at Info and at
// Debug thereafter: every poll cycle reaches this branch for every repo routed
// to the unreachable instance, and an operator needs the fact, not one line
// per cycle.
func (cs *clusterState) noteDeferral(repo, op string, inst instances.Instance) {
	msg := "cluster: owning instance is unreachable but not confirmed down; leaving its work to it"
	args := []any{
		"repo", repo, "operation", op, "instance", inst.ID,
		"failed_probes", cs.probeFailures(inst.ID),
		"takeover_after_failed_probes", cs.takeoverThreshold(),
	}
	if cs.notes.claim(inst.ID, noticeSubject("defer", dispatchUnit(op, repo))) {
		slog.Info(msg, args...)
		return
	}
	slog.Debug(msg, args...)
}

// notAssignedNotesBucket is the instanceNotes key noteNotAssigned dedups
// under. There is deliberately no real instance id to key by: verdictNotAssigned
// covers repos with no resolvable owner at all (an unassigned repo with no
// default_instance pushed yet), not only ones naming an instance this worker
// cannot see in its own (empty, by design) registry.
//
// Leading "#" is load-bearing, not decoration: config.ValidateInstanceID
// requires an alphanumeric first character, so this string can never be a
// real instance id an operator registers — a plain "unassigned" could,
// which would silently collide this dedup bucket with that instance's own
// deferral/dispatch-failure notices. PR review feedback (#770).
const notAssignedNotesBucket = "#unassigned"

// noteNotAssigned reports that this worker deliberately left repo alone
// because the hub has not assigned it here — as opposed to verdictActLocally,
// which means there is genuinely nobody else to defer to. Silence here is
// what an operator would read as "this worker just isn't reviewing
// anything", so the first report per (operation, repo) is a WARN; later polls
// of the same, still-unassigned unit log at Debug, the same dedup discipline
// noteDeferral and noteDispatchFailure already use.
//
// Cleared, like every other note, on the next config reload — which is fine
// here: a repo still unassigned after a reload is exactly the case worth
// reporting again, not a repeat to swallow.
func (cs *clusterState) noteNotAssigned(repo, op string) {
	msg := "cluster: this worker has no partition rule for repo yet; waiting for the hub to push one (PUT /cluster/partition) instead of reviewing it"
	args := []any{"repo", repo, "operation", op}
	if cs.notes.claim(notAssignedNotesBucket, noticeSubject("not_assigned", dispatchUnit(op, repo))) {
		slog.Warn(msg, args...)
		return
	}
	slog.Debug(msg, args...)
}

// announceTakeover reports, once per (instance, operation, repo) outage, that
// this daemon has stopped deferring to another instance and is doing its work.
//
// This exists because #765 was silent. The operator saw "instance became
// unreachable", which reads as *that instance is not working* — not as *that
// instance is working twice*. The three reasons need different words because
// they need different fixes: an unanswered probe most often means a base_url
// holding an address DHCP has since moved, while a rejected RPC means the
// instance is up and refusing the work, which is a token or config problem on
// the remote.
func (cs *clusterState) announceTakeover(repo, op string, inst instances.Instance, reason takeoverReason) {
	label := reason.label()
	if !cs.notes.claim(inst.ID, noticeSubject("takeover:"+label, dispatchUnit(op, repo))) {
		return
	}
	failures := cs.probeFailures(inst.ID)
	args := []any{
		"repo", repo, "operation", op, "instance", inst.ID, "instance_name", inst.Name,
		"failed_probes", failures,
	}
	switch reason {
	case takeoverProbesFailed:
		slog.Warn("cluster: taking over work from an instance that has stopped answering health probes; "+
			"if it is alive and merely unreachable from here it is still reviewing this repository, so this work is "+
			"being duplicated (the publish-boundary check should stop a second review from being posted, but the AI "+
			"spend is already incurred)",
			append(args, "base_url_hint", "cluster.instances."+inst.ID+".base_url")...)
	case takeoverDispatchRejected:
		slog.Warn("cluster: taking over work from an instance that answers health probes but keeps rejecting our "+
			"dispatch calls; it is running and refusing this work, so check its cluster token and whether the "+
			"repository is configured on that instance at all",
			append(args, "token_hint", "cluster.instances."+inst.ID+".token")...)
	default: // takeoverNoObserver
		// Not a hub: no prober, no health history, nothing to duplicate on the
		// strength of. This is the plain pre-#765 local fallback, so it is not
		// the operator alarm the other two are, and there is no /instances UI
		// on a worker for an SSE event to reach.
		//
		// It still consumes a claim, because without one this line would repeat
		// for every routed repo on every poll cycle. A daemon with no prober
		// also has no recovery callback, so the claim can only be released by
		// clearUnit on the next successful dispatch — which is the right
		// release for this path: while the RPC keeps failing there is one
		// continuous condition to report, not a new one each cycle, and the
		// per-cycle detail stays available at Debug in noteDispatchFailure.
		slog.Info("cluster: dispatch failed and this daemon does not probe its peers; handling the work locally",
			args...)
		return
	}
	if cs.broker == nil {
		return
	}
	cs.broker.Publish(sse.Event{
		Type: sse.EventInstanceTakeover,
		Data: sseData(map[string]any{
			"instance_id":   inst.ID,
			"instance_name": inst.Name,
			"repo":          repo,
			"operation":     op,
			"reason":        label,
			"failed_probes": failures,
		}),
	})
}

// OwnerCanHandle reports whether repo is routed to another instance this daemon
// should leave alone with work it cannot dispatch as a single RPC call — issue
// triage, whose individual items are not known until deep inside per-repo
// processing, unlike a PR review which has one PR id to hand off.
//
// false means either there is no routing away from this daemon, or its owner is
// unregistered or confirmed down — in all of those cases the caller must act
// locally instead of leaving the repo's issues unattended. An owner that is
// merely unreachable returns true, for the reason spelled out on
// ownerVerdictFor: it is still triaging its own repos.
func (cs *clusterState) OwnerCanHandle(repo string) bool {
	inst, verdict := cs.ownerVerdictFor(repo)
	switch verdict {
	case verdictTakeOver:
		cs.announceTakeover(repo, "issue_triage", inst, takeoverProbesFailed)
		return false
	case verdictDeferToOwner:
		// Recorded through the same deduped channel dispatch uses. Leaving
		// work to an unreachable peer is a state an operator needs to see, and
		// issue triage was taking that decision with no record at all while
		// the PR path logged it.
		cs.noteDeferral(repo, "issue_triage", inst)
		return true
	case verdictNotAssigned:
		// Same reasoning as dispatch's case: this worker is not the one to
		// triage repo's issues, whether or not anyone else currently is.
		cs.noteNotAssigned(repo, "issue_triage")
		return true
	default:
		return verdict == verdictDispatch
	}
}

// DispatchPRReview hands a PR review to the instance repo is routed to.
// true means the caller must not review the PR locally — see dispatch for the
// two ways that happens.
func (cs *clusterState) DispatchPRReview(ctx context.Context, repo string, prID int64, prURL string) bool {
	return cs.dispatch(ctx, repo, "review", func(client *instances.Client) error {
		// An instance that does not own the repo has never seen the PR, so it
		// has to adopt it before it can review it. Ignoring an add failure is
		// deliberate: the PR may already be known there, in which case the
		// review call below is the one whose result matters.
		if prURL != "" {
			if _, err := client.AddPR(ctx, prURL); err != nil {
				slog.Debug("cluster: add-PR before dispatched review failed", "repo", repo, "err", err)
			}
		}
		return client.TriggerPRReview(ctx, prID)
	})
}

// DispatchIssueReview hands issue-triage work to the instance repo is routed
// to. true means the caller must not process it locally.
func (cs *clusterState) DispatchIssueReview(ctx context.Context, repo string, issueID int64) bool {
	return cs.dispatch(ctx, repo, "issue_review", func(client *instances.Client) error {
		return client.TriggerIssueReview(ctx, issueID)
	})
}

// propagateClusterConfig pushes the hub's on-disk config to every healthy
// non-self instance and publishes EventConfigPropagated so the GUI reflects
// the result without an operator having to open the manual propagate dialog.
//
// Before this, config sync only ever happened when someone remembered to
// trigger it by hand — which is how 14 keys (ai_primary, agent_configs,
// org_overrides, repo_overrides, poll_interval...) silently drifted between
// a hub and a worker with nobody noticing. Reads straight from cfgPath rather
// than a decoded *config.Config so operator-only keys and TOML structure
// survive the round trip exactly like the manual /cluster/propagate endpoint.
func propagateClusterConfig(ctx context.Context, cfgPath string, cs *clusterState, broker *sse.Broker) {
	source, err := config.ReadTOMLMap(cfgPath)
	if err != nil {
		slog.Warn("cluster: could not read config for automatic propagation", "path", cfgPath, "err", err)
		return
	}
	filtered, _ := instances.FilterPropagatable(source)

	snap := cs.Snapshot()
	results := snap.Propagator.Propagate(ctx, filtered, nil)

	for _, res := range results {
		if !res.OK && !res.Skipped {
			slog.Warn("cluster: automatic config propagation failed",
				"instance", res.InstanceID, "err", res.Error)
		}
	}
	if broker != nil {
		broker.Publish(sse.Event{
			Type: sse.EventConfigPropagated,
			Data: sseData(map[string]any{"results": results}),
		})
	}
}

// propagatePartition pushes the ownership partition — this instance's
// assigned identity, default_instance and routing.orgs/repos — to every
// usable instance in the registry, each under its own id.
//
// This is the mechanism that actually closes theburrowhub/heimdallm#769: a
// worker with no partition of its own fails closed (Router.worker treats "no
// resolvable owner" as "not mine", not "act locally" — see Owns), so it needs
// one of these pushes to own anything at all. Called alongside
// propagateClusterConfig on every hub reload; the two never race over the
// same keys — FilterPropagatable always strips cluster.* from the general
// push, and this one touches nothing else — so there is nothing to order
// between them.
func propagatePartition(ctx context.Context, cs *clusterState) {
	snap := cs.Snapshot()
	if snap.Router == nil {
		return
	}
	rules := snap.Router.RulesSnapshot()
	results := snap.Propagator.PropagatePartition(ctx, instances.PartitionRules{
		DefaultInstance: snap.Router.Fallback(),
		Orgs:            rules.Orgs,
		Repos:           rules.Repos,
	}, nil)

	for _, res := range results {
		switch {
		case res.IdentityMismatch:
			// This is the Friday case, surfaced: registered under one id,
			// reporting itself as another. Neither push path can enforce a
			// partition an instance does not recognise as its own — the
			// operator has to fix the mismatch (update cluster.instances.*
			// to the reported id, or restart the instance so it adopts the
			// registered one).
			//
			// res.Error is included even though the withheld-on-legacy path
			// already folds this same explanation into it: a mismatch
			// observed alongside an UNRELATED push failure (a network error,
			// a 500) would otherwise have that failure silently dropped —
			// only this warning logged, with nothing pointing at the actual
			// cause. PR review feedback (#770).
			slog.Warn("cluster: instance is registered under one id but reports itself as another; its partition cannot be enforced until this is fixed",
				"instance", res.InstanceID, "instance_name", res.Name,
				"hint", "cluster.instances."+res.InstanceID, "err", res.Error)
		case !res.OK && !res.Skipped:
			slog.Warn("cluster: partition propagation failed", "instance", res.InstanceID, "instance_name", res.Name, "err", res.Error)
		case res.Legacy && res.OK:
			slog.Info("cluster: instance predates PUT /cluster/partition; its rules were pushed through the legacy PATCH /config path instead",
				"instance", res.InstanceID)
		}
	}
}

// resolveReloadInstanceID keeps this daemon's identity stable across reloads,
// with two distinct jobs.
//
// The original one: recover from booting before [cluster] existed. If
// identity was never resolved (current is empty, because clustering was off
// when ensureInstanceID ran at boot) and the newly reloaded config now wants
// clustering, resolve — and, if needed, generate and persist — an identity
// now instead of leaving it stuck at "" until the next full process restart.
// A "" selfID makes Router.Owns fail open (it treats an unidentifiable daemon
// as owning everything), which is what let a hub silently keep reviewing
// repos that were routed away from it after an operator added [cluster]
// without restarting.
//
// The second: adopt an identity the hub assigned. PUT /cluster/partition
// writes cluster.instance_id directly into a worker's config.toml — it is how
// the hub reconciles a mismatch like theburrowhub/heimdallm#769's, where an
// instance was registered under a slugified address ("192-168-1-100-3000")
// while it reported itself as something else ("friday") — and the next
// reload must pick that value up without a restart, the same way the first
// job avoids one. Restricted to a non-hub: a hub's identity changing under it
// in place would leave its own registry and every routing rule still naming
// the old id, silently orphaning everything the hub itself owns.
func resolveReloadInstanceID(current string, newCfg *config.Config, dataDir string) (string, error) {
	explicit := strings.TrimSpace(newCfg.Cluster.InstanceID)
	if explicit != "" && explicit != current && !newCfg.IsHub() {
		if err := config.ValidateInstanceID(explicit); err != nil {
			return "", err
		}
		if err := persistInstanceID(dataDir, explicit); err != nil {
			return "", err
		}
		if current != "" {
			slog.Warn("cluster: adopting the instance id assigned by the hub", "was", current, "now", explicit)
		}
		return explicit, nil
	}
	if strings.TrimSpace(current) != "" || !newCfg.ClusterEnabled() {
		return current, nil
	}
	return ensureInstanceID(newCfg, dataDir)
}

// persistInstanceID writes id to <dataDir>/instance_id, overwriting whatever
// was there.
//
// Deliberately O_TRUNC rather than ensureInstanceID's O_CREATE|O_EXCL: that
// exclusivity resolves a startup race between two processes generating an id
// at the same time by making the loser adopt the winner's value. Here the
// value comes from outside (the hub) and must win outright — if it lost to a
// stale file, an operator hand-editing or wiping config.toml back to the old
// identity would keep this daemon reviewing under an id the hub no longer
// recognises as the owner of anything, reopening the exact bug this call
// exists to close.
func persistInstanceID(dir, id string) error {
	path := filepath.Join(dir, "instance_id")
	if err := os.WriteFile(path, []byte(id+"\n"), instanceIDFileMode); err != nil {
		return fmt.Errorf("instance_id: persist %s: %w", path, err)
	}
	return nil
}

// Router returns the live router. Never nil.
func (cs *clusterState) Router() *instances.Router {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.router
}

// Owns reports whether this daemon should act on repo. This is the single guard
// that partitions autonomous work between instances.
func (cs *clusterState) Owns(repo string) bool {
	if cs == nil {
		return true
	}
	return cs.Router().Owns(repo)
}

// FilterOwned narrows a repo list to what this daemon owns.
func (cs *clusterState) FilterOwned(repos []string) []string {
	if cs == nil {
		return repos
	}
	return cs.Router().FilterOwned(repos)
}

// Snapshot supplies the HTTP handlers with the current control-plane view.
func (cs *clusterState) Snapshot() server.ClusterSnapshot {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return server.ClusterSnapshot{
		Registry:   cs.registry,
		Router:     cs.router,
		Propagator: instances.NewPropagator(cs.registry, cs.factory),
		Role:       cs.role,
		SelfID:     cs.selfID,
		SelfName:   cs.selfName,
	}
}

// Identity returns this daemon's id, name and role for GET /health.
func (cs *clusterState) Identity() (id, name, role string) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.selfID, cs.selfName, cs.role
}

// Prober returns the health prober, or nil when this daemon is not a hub.
func (cs *clusterState) Prober() *instances.Prober {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.prober
}

// RunProber drives the health loop until ctx is cancelled. No-op on a worker.
func (cs *clusterState) RunProber(ctx context.Context) {
	if p := cs.Prober(); p != nil {
		p.Run(ctx)
	}
}

// clusterEvents publishes instance up/down transitions onto the SSE stream so
// the GUI can react without polling.
type clusterEvents struct {
	broker *sse.Broker
	// onRecovered is called with the instance id whenever a probe flips an
	// instance back to reachable. Optional; nil in tests that only assert on
	// the SSE stream.
	onRecovered func(instanceID string)
}

// newClusterEvents wires the prober's transition callback. It returns nil only
// when there is nothing at all to do — a non-nil onRecovered matters even
// without a broker, because the notes have to be cleared on recovery whether
// or not anyone is watching the SSE stream.
func newClusterEvents(broker *sse.Broker, onRecovered func(string)) instances.EventPublisher {
	if broker == nil && onRecovered == nil {
		return nil
	}
	return &clusterEvents{broker: broker, onRecovered: onRecovered}
}

func (e *clusterEvents) InstanceStateChanged(s instances.State) {
	// Clear what we remember about an instance the moment it comes back, so a
	// later outage is reported as a new event rather than swallowed as a repeat
	// of the last one.
	//
	// This hangs off the prober's transition callback rather than off
	// ownerVerdictFor's healthy branch, and the difference is not cosmetic:
	// that branch runs on every poll cycle for a healthy instance, so clearing
	// there reset the consecutive-dispatch-failure count before it could ever
	// reach the threshold — an owner rejecting every RPC would have been
	// deferred to forever.
	if s.Reachable && e.onRecovered != nil {
		e.onRecovered(s.InstanceID)
	}
	if e.broker == nil {
		return
	}
	eventType := sse.EventInstanceDown
	if s.Reachable {
		eventType = sse.EventInstanceUp
	}
	e.broker.Publish(sse.Event{
		Type: eventType,
		Data: sseData(map[string]any{
			"instance_id":   s.InstanceID,
			"instance_name": s.Name,
			"reachable":     s.Reachable,
			"version":       s.Version,
			"error":         s.LastError,
		}),
	})
}

// clusterTakeoverThreshold resolves how many consecutive failed probes must
// pass before the hub does a routed owner's work. Mirrors clusterProbeInterval:
// an unset or nonsensical value falls back to the documented default rather
// than to zero, which a bare >= comparison would read as "take over now".
func clusterTakeoverThreshold(cfg *config.Config) int {
	if cfg == nil || cfg.Cluster.TakeoverAfterFailedProbes == nil || *cfg.Cluster.TakeoverAfterFailedProbes < 1 {
		return config.DefaultTakeoverAfterFailedProbes
	}
	return *cfg.Cluster.TakeoverAfterFailedProbes
}

func clusterProbeInterval(cfg *config.Config) time.Duration {
	d, err := time.ParseDuration(cfg.Cluster.ProbeInterval)
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}

// resolvedSelfName returns the display name for this daemon, defaulting to the
// hostname so an operator adding a second machine sees something meaningful
// without having to name it first.
func resolvedSelfName(cfg *config.Config) string {
	if name := strings.TrimSpace(cfg.Cluster.InstanceName); name != "" {
		return name
	}
	// No identity to name: a daemon outside a cluster has no instance to label.
	if !cfg.ClusterEnabled() {
		return ""
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return cfg.Cluster.InstanceID
}

// ensureInstanceID resolves this daemon's stable identity.
//
// Precedence: an explicit [cluster].instance_id, then a previously generated id
// persisted next to the API token, then a freshly generated one. Persisting it
// in the data directory rather than config.toml means the id survives an
// operator rewriting or deleting their config, which is exactly when a hub must
// not suddenly consider this a different machine.
//
// Uses the same O_CREATE|O_EXCL race handling as loadOrCreateAPIToken: if two
// processes start together, the loser adopts the winner's id instead of the two
// silently diverging.
func ensureInstanceID(cfg *config.Config, dir string) (string, error) {
	if id := strings.TrimSpace(cfg.Cluster.InstanceID); id != "" {
		if err := config.ValidateInstanceID(id); err != nil {
			return "", err
		}
		return id, nil
	}
	// A daemon that is not part of a cluster does not need an identity, and
	// generating one would write a file into the data dir of every single
	// install for a feature they are not using.
	if !cfg.ClusterEnabled() {
		return "", nil
	}

	path := filepath.Join(dir, "instance_id")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); config.ValidateInstanceID(id) == nil {
			return id, nil
		}
		// A truncated or hand-edited file must not wedge the daemon at boot.
		// Remove it first: the exclusive create below would otherwise fail with
		// "file exists" and the race-recovery path would just re-read the same
		// unusable value.
		slog.Warn("instance_id: stored value is unusable, regenerating", "path", path)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("instance_id: could not replace the unusable file %s: %w", path, err)
		}
	}

	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("instance_id: generate random: %w", err)
	}
	id := instanceIDPrefix() + hex.EncodeToString(buf)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, instanceIDFileMode)
	if err != nil {
		if os.IsExist(err) {
			// Another process created the file between our read and here: adopt
			// its id rather than letting the two silently diverge.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", fmt.Errorf("instance_id: read after race: %w", readErr)
			}
			if existing := strings.TrimSpace(string(data)); config.ValidateInstanceID(existing) == nil {
				return existing, nil
			}
			return "", fmt.Errorf("instance_id: %s exists but holds an unusable id; remove it and restart", path)
		}
		return "", fmt.Errorf("instance_id: create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s\n", id); err != nil {
		return "", fmt.Errorf("instance_id: write %s: %w", path, err)
	}
	slog.Info("instance_id: generated a new instance identity", "id", id, "path", path)
	return id, nil
}

// instanceIDPrefix derives a short, readable prefix from the hostname so ids
// look like "mbp-3f2a91..." rather than opaque hex in logs and the UI.
func instanceIDPrefix() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "node-"
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if len(b.String()) >= 12 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	prefix := strings.Trim(b.String(), "-")
	if prefix == "" || !(prefix[0] >= 'a' && prefix[0] <= 'z') {
		return "node-"
	}
	return prefix + "-"
}

// wireCluster installs the control plane on the HTTP server.
//
// The identity is always published (a worker must be recognisable on /health),
// but ClusterDeps — and therefore the /instances and /cluster routes — only on
// a hub.
func wireCluster(srv *server.Server, cs *clusterState, st *store.Store) {
	id, name, role := cs.Identity()
	// A daemon that is not part of a cluster publishes nothing: /health must
	// look exactly as it did before instances existed, with no identity fields
	// for a feature the operator is not using.
	if id == "" && role == "" {
		return
	}
	srv.SetClusterIdentity(id, name, role)

	if !strings.EqualFold(role, config.RoleHub) {
		return
	}
	deps := &server.ClusterDeps{
		Snapshot:   cs.Snapshot,
		Prober:     cs.Prober(),
		Discoverer: cs.Discoverer(),
		NewClient:  cs.factory,
	}
	if st != nil {
		deps.Store = st
	}
	srv.SetCluster(deps)
}
