// Package workgate coordinates long-running daemon work with process updates.
package workgate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Kind identifies work that must finish before the daemon may be replaced.
type Kind string

const (
	KindReview              Kind = "reviews"
	KindIssue               Kind = "issue_runs"
	KindImplementation      Kind = "implementations"
	KindReviewResponse      Kind = "review_responses"
	KindReviewFix           Kind = "review_fixes"
	KindAutonomous          Kind = "autonomous_runs"
	KindPublish             Kind = "review_publications"
	KindState               Kind = "state_checks"
	KindMaintenance         Kind = "maintenance"
	maxLeaseIDBytes              = 128
	maxPersistentLeaseBytes      = 4 << 10
	openPollInterval             = 25 * time.Millisecond
)

var (
	// ErrDraining means an updater owns the admission gate. Callers should treat
	// it as an expected pause, not as a failed review or implementation.
	ErrDraining = errors.New("workgate: update drain in progress")
	// ErrLeaseIDRequired means an updater did not identify its drain lease.
	ErrLeaseIDRequired = errors.New("workgate: update lease id required")
	// ErrLeaseIDInvalid means the owner token is too large to persist safely.
	ErrLeaseIDInvalid = errors.New("workgate: update lease id is invalid")
	// ErrLeaseConflict means another updater owns the current drain lease.
	ErrLeaseConflict = errors.New("workgate: update lease owned by another client")
	// ErrWorkActive means the owner tried to seal replacement before every
	// admitted transaction had completed.
	ErrWorkActive = errors.New("workgate: active work remains")
	// ErrLeaseNotSealed means the owner tried to authorize replacement
	// bootstrap before committing the non-expiring barrier.
	ErrLeaseNotSealed = errors.New("workgate: update lease is not sealed")
	// ErrBootstrapNotAuthorized prevents a late DELETE acknowledged by one
	// daemon instance from opening a seal restored by a different respawn. The
	// replacement owner must confirm every process instance before it may open
	// that instance's durable barrier.
	ErrBootstrapNotAuthorized = errors.New("workgate: replacement bootstrap is not authorized")
)

// Snapshot is an immutable view of the update drain and active work.
type Snapshot struct {
	Draining bool
	Sealed   bool
	// BootstrapAuthorized is deliberately process-local. A replacement daemon
	// must receive a fresh owner-authenticated confirmation after every crash;
	// persisting it would let an unattended respawn run migrations before the
	// desktop app has re-verified the executable and bundled version.
	BootstrapAuthorized bool
	LeaseID             string
	LeaseExpiresAt      time.Time
	Active              map[Kind]int
}

// Total returns the number of protected operations still running.
func (s Snapshot) Total() int {
	total := 0
	for _, count := range s.Active {
		total += count
	}
	return total
}

// Permit represents one admitted top-level operation. It is deliberately
// opaque: nested pipelines can reuse it, but cannot forge or mutate it.
type Permit struct {
	gate     *Gate
	kind     Kind
	released atomic.Bool
}

// Release ends the admitted operation. It is idempotent so cleanup paths can
// safely converge without corrupting another concurrent operation's count.
func (p *Permit) Release() {
	if p == nil || p.gate == nil || !p.released.CompareAndSwap(false, true) {
		return
	}
	p.gate.release(p.kind)
}

type permitContextKey struct{}

// WithPermit carries an outer admission permit into nested pipelines.
func WithPermit(ctx context.Context, permit *Permit) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if permit == nil {
		return ctx
	}
	return context.WithValue(ctx, permitContextKey{}, permit)
}

// PermitFromContext returns the outer admission permit, if any.
func PermitFromContext(ctx context.Context) *Permit {
	if ctx == nil {
		return nil
	}
	permit, _ := ctx.Value(permitContextKey{}).(*Permit)
	return permit
}

type persistedLease struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Sealed    bool      `json:"sealed,omitempty"`
}

// Gate grants work permits while running normally and atomically blocks new
// work once an updater starts draining the daemon. The drain is leased: if the
// desktop app crashes before installation, normal work resumes automatically.
// A persistent gate restores a still-live lease after a launchd respawn.
type Gate struct {
	mu                  sync.Mutex
	active              map[Kind]int
	draining            bool
	sealed              bool
	bootstrapAuthorized bool
	leaseID             string
	leaseExpiresAt      time.Time
	leaseTTL            time.Duration
	statePath           string
	now                 func() time.Time
	syncDir             func(string) error
}

// New creates an in-memory gate whose update preparation lease lasts leaseTTL.
// Updaters renew the lease by calling Prepare again with the same lease ID.
func New(leaseTTL time.Duration) *Gate {
	g, err := newGate(leaseTTL, "")
	if err != nil {
		panic(err)
	}
	return g
}

// NewPersistent creates a gate backed by an atomically replaced state file.
// A valid, unexpired marker restores the drain before any daemon worker starts.
func NewPersistent(leaseTTL time.Duration, statePath string) (*Gate, error) {
	if strings.TrimSpace(statePath) == "" {
		return nil, fmt.Errorf("workgate: persistent state path is empty")
	}
	return newGate(leaseTTL, statePath)
}

func newGate(leaseTTL time.Duration, statePath string) (*Gate, error) {
	if leaseTTL <= 0 {
		return nil, fmt.Errorf("workgate: lease TTL must be positive")
	}
	g := &Gate{
		active:    make(map[Kind]int),
		leaseTTL:  leaseTTL,
		statePath: statePath,
		now:       time.Now,
		syncDir:   syncDirectory,
	}
	if statePath != "" {
		if err := g.restore(); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// Acquire starts protected top-level work. It fails while an update drain has
// a live lease, closing the check-then-shutdown race that could otherwise start
// a new review after the updater observed an idle daemon.
func (g *Gate) Acquire(kind Kind) (*Permit, error) {
	if g == nil {
		return nil, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLeaseLocked()
	if g.draining {
		return nil, ErrDraining
	}
	g.active[kind]++
	return &Permit{gate: g, kind: kind}, nil
}

// AcquireContext reuses a valid outer permit from ctx or admits a new
// top-level operation and returns a context carrying it. The boolean reports
// whether the caller owns (and therefore must Release) the returned permit.
func (g *Gate) AcquireContext(ctx context.Context, kind Kind) (context.Context, *Permit, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if g == nil {
		return ctx, nil, false, nil
	}
	if permit := PermitFromContext(ctx); g.Accepts(permit) {
		return ctx, permit, false, nil
	}
	permit, err := g.Acquire(kind)
	if err != nil {
		return ctx, nil, false, err
	}
	return WithPermit(ctx, permit), permit, true, nil
}

// Accepts reports whether permit is a live admission from this gate.
func (g *Gate) Accepts(permit *Permit) bool {
	return g != nil && permit != nil && permit.gate == g && !permit.released.Load()
}

func (g *Gate) release(kind Kind) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[kind] <= 1 {
		delete(g.active, kind)
		return
	}
	g.active[kind]--
}

// Prepare starts or renews a drain owned by leaseID and returns the operations
// still active. The client creates a cryptographically random leaseID before
// its first request and reuses it for every renewal, making a lost first
// response idempotent. A different owner can never renew the live lease.
func (g *Gate) Prepare(leaseID string) (Snapshot, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return Snapshot{}, ErrLeaseIDRequired
	}
	if len(leaseID) > maxLeaseIDBytes {
		return Snapshot{}, ErrLeaseIDInvalid
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLeaseLocked()
	if g.draining && g.leaseID != leaseID {
		return g.snapshotLocked(), ErrLeaseConflict
	}
	// Once the updater has durably sealed the idle daemon for replacement,
	// ordinary renewals from that same owner must never turn the marker back
	// into an expiring lease. This is what keeps a slow Sparkle install or user
	// authorization prompt from reopening work on the replacement daemon.
	if g.sealed {
		return g.snapshotLocked(), nil
	}

	expiresAt := g.now().Add(g.leaseTTL)
	if err := g.persistLocked(persistedLease{LeaseID: leaseID, ExpiresAt: expiresAt}); err != nil {
		return g.snapshotLocked(), err
	}
	g.draining = true
	g.leaseID = leaseID
	g.leaseExpiresAt = expiresAt
	return g.snapshotLocked(), nil
}

// Seal converts a live, owner-authenticated drain into a non-expiring
// replacement barrier. The updater calls it only after the daemon is idle and
// after its native recovery journal is durable, immediately before stopping
// the daemon. The same owner must explicitly Cancel the barrier after the new
// app and daemon versions have been verified.
func (g *Gate) Seal(leaseID string) (Snapshot, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return Snapshot{}, ErrLeaseIDRequired
	}
	if len(leaseID) > maxLeaseIDBytes {
		return Snapshot{}, ErrLeaseIDInvalid
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLeaseLocked()
	if !g.draining || g.leaseID != leaseID {
		return g.snapshotLocked(), ErrLeaseConflict
	}
	if g.sealed {
		return g.snapshotLocked(), nil
	}
	if snapshot := g.snapshotLocked(); snapshot.Total() != 0 {
		return snapshot, ErrWorkActive
	}
	if err := g.persistLocked(persistedLease{LeaseID: leaseID, Sealed: true}); err != nil {
		return g.snapshotLocked(), err
	}
	g.sealed = true
	g.leaseExpiresAt = time.Time{}
	return g.snapshotLocked(), nil
}

// ConfirmBootstrap authorizes a replacement daemon to initialize its
// stateful dependencies while the admission gate remains sealed. This is the
// second half of the replacement handshake: the native updater first verifies
// the restored daemon's PID, executable and version, then confirms bootstrap;
// it cancels the seal only after /health reports the fully initialized daemon.
//
// Authorization is intentionally not persisted. If bootstrap crashes, the
// next daemon must remain at the minimal HTTP surface until the owner verifies
// that new process again.
func (g *Gate) ConfirmBootstrap(leaseID string) (Snapshot, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return Snapshot{}, ErrLeaseIDRequired
	}
	if len(leaseID) > maxLeaseIDBytes {
		return Snapshot{}, ErrLeaseIDInvalid
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLeaseLocked()
	if !g.draining || g.leaseID != leaseID {
		return g.snapshotLocked(), ErrLeaseConflict
	}
	if !g.sealed {
		return g.snapshotLocked(), ErrLeaseNotSealed
	}
	g.bootstrapAuthorized = true
	return g.snapshotLocked(), nil
}

// Cancel abandons the identified drain and permits new work immediately. It is
// idempotent when no lease is active; a foreign owner can never open a live
// drain. The response echoes leaseID so clients can authenticate rollback.
func (g *Gate) Cancel(leaseID string) (Snapshot, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return Snapshot{}, ErrLeaseIDRequired
	}
	if len(leaseID) > maxLeaseIDBytes {
		return Snapshot{}, ErrLeaseIDInvalid
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLeaseLocked()
	if !g.draining {
		snapshot := g.snapshotLocked()
		snapshot.LeaseID = leaseID
		return snapshot, nil
	}
	if g.leaseID != leaseID {
		return g.snapshotLocked(), ErrLeaseConflict
	}
	if g.sealed && !g.bootstrapAuthorized {
		return g.snapshotLocked(), ErrBootstrapNotAuthorized
	}
	if err := g.removeStateLocked(); err != nil {
		return g.snapshotLocked(), err
	}
	g.clearLeaseLocked()
	snapshot := g.snapshotLocked()
	// Echo the authenticated owner as a cancellation acknowledgement even
	// though no live lease remains. Clients must verify this before rollback.
	snapshot.LeaseID = leaseID
	return snapshot, nil
}

// Status reports current state, expiring an updater lease whose owner stopped
// renewing it.
func (g *Gate) Status() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLeaseLocked()
	return g.snapshotLocked()
}

// WaitUntilBootstrapAuthorized blocks stateful bootstrap while a restored
// drain is active. An ordinary abandoned lease must expire or be cancelled. A
// sealed replacement may initialize only after its owner confirms the exact
// restored process; importantly, that confirmation does not open admission.
func (g *Gate) WaitUntilBootstrapAuthorized(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(openPollInterval)
	defer ticker.Stop()
	for {
		snapshot := g.Status()
		if !snapshot.Draining || (snapshot.Sealed && snapshot.BootstrapAuthorized) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (g *Gate) expireLeaseLocked() {
	if !g.draining || g.sealed || g.leaseExpiresAt.After(g.now()) {
		return
	}
	// An expired marker is harmless on restart because restore also checks its
	// timestamp. Removal is best effort so a transient filesystem problem cannot
	// leave the running daemon drained forever.
	if err := g.removeStateLocked(); err != nil {
		// Intentionally no logging in this low-level package; the stale marker is
		// fail-open only after its recorded expiry and is safe to clean next time.
		_ = err
	}
	g.clearLeaseLocked()
}

func (g *Gate) clearLeaseLocked() {
	g.draining = false
	g.sealed = false
	g.bootstrapAuthorized = false
	g.leaseID = ""
	g.leaseExpiresAt = time.Time{}
}

func (g *Gate) snapshotLocked() Snapshot {
	active := make(map[Kind]int, len(g.active))
	for kind, count := range g.active {
		active[kind] = count
	}
	return Snapshot{
		Draining:            g.draining,
		Sealed:              g.sealed,
		BootstrapAuthorized: g.bootstrapAuthorized,
		LeaseID:             g.leaseID,
		LeaseExpiresAt:      g.leaseExpiresAt,
		Active:              active,
	}
}

func (g *Gate) restore() error {
	data, err := readPrivateRegularFile(g.statePath, maxPersistentLeaseBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workgate: read persistent lease securely: %w", err)
	}
	var state persistedLease
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("workgate: decode persistent lease: %w", err)
	}
	state.LeaseID = strings.TrimSpace(state.LeaseID)
	if state.LeaseID == "" || len(state.LeaseID) > maxLeaseIDBytes || (!state.Sealed && state.ExpiresAt.IsZero()) {
		return fmt.Errorf("workgate: persistent lease is incomplete")
	}
	if state.Sealed {
		g.draining = true
		g.sealed = true
		g.leaseID = state.LeaseID
		g.leaseExpiresAt = time.Time{}
		return nil
	}
	if !state.ExpiresAt.After(g.now()) {
		if err := g.removeStateLocked(); err != nil {
			return fmt.Errorf("workgate: remove expired persistent lease: %w", err)
		}
		return nil
	}
	// A replacement daemon may spend most of the predecessor's remaining TTL
	// in startup. Renew durably before admitting any subsystem; the authenticated
	// updater can continue renewing through the startup route as soon as Serve
	// begins.
	state.ExpiresAt = g.now().Add(g.leaseTTL)
	if err := g.persistLocked(state); err != nil {
		return fmt.Errorf("workgate: renew restored persistent lease: %w", err)
	}
	g.draining = true
	g.leaseID = state.LeaseID
	g.leaseExpiresAt = state.ExpiresAt
	return nil
}

func (g *Gate) persistLocked(state persistedLease) error {
	if g.statePath == "" {
		return nil
	}
	dir := filepath.Dir(g.statePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("workgate: create persistent lease directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".update-drain-*")
	if err != nil {
		return fmt.Errorf("workgate: create persistent lease temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("workgate: secure persistent lease temp file: %w", err)
	}
	enc := json.NewEncoder(tmp)
	if err := enc.Encode(state); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("workgate: encode persistent lease: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("workgate: sync persistent lease: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("workgate: close persistent lease: %w", err)
	}
	if err := os.Rename(tmpPath, g.statePath); err != nil {
		return fmt.Errorf("workgate: replace persistent lease: %w", err)
	}
	if err := g.syncDir(dir); err != nil {
		return fmt.Errorf("workgate: sync persistent lease directory: %w", err)
	}
	return nil
}

func (g *Gate) removeStateLocked() error {
	if g.statePath == "" {
		return nil
	}
	if err := os.Remove(g.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workgate: remove persistent lease: %w", err)
	}
	dir := filepath.Dir(g.statePath)
	if err := g.syncDir(dir); err != nil {
		return fmt.Errorf("workgate: sync persistent lease directory: %w", err)
	}
	return nil
}

func syncDirectory(dir string) error {
	dirHandle, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := dirHandle.Sync()
	closeErr := dirHandle.Close()
	return errors.Join(syncErr, closeErr)
}
