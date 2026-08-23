// Package procgroup runs an *exec.Cmd in a process group owned by Heimdallm so
// cancellation and shutdown can terminate the command and every descendant
// that remains in that group.
//
// A dedicated sentinel is the group leader. The command joins that group and
// the sentinel is deliberately not reaped until the final group signal has
// been delivered. Cancellation uses SIGTERM, a grace period, then SIGKILL;
// ordinary completion kills residual helpers immediately. Keeping the leader
// as a live process or an unreaped zombie reserves both its PID and the PGID,
// so a late escalation can never be delivered to an unrelated process group
// that happened to reuse the numeric ID.
//
// Descendants that have already died still need PID 1 to reap them. The init
// process in docker/Dockerfile handles that container concern; this package's
// responsibility is to leave no live Heimdallm-owned group member behind.
//
// The ownership boundary is deliberately the process group, not arbitrary
// ancestry: a program that explicitly calls setsid or setpgid has opted out of
// the group and cannot be reached by kill(-pgid, ...). Shared services such as
// an SSH ControlPersist master may have that independent lifetime; they do not
// keep the git transaction or its repository work registered as in flight.
package procgroup

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	// TermGrace is how long a process group gets to handle SIGTERM before it is
	// killed outright. It is exported for Executor.TerminateAll, which sends
	// TERM to all groups before escalating them together.
	TermGrace = 1 * time.Second
	// WaitDelay bounds exec.Cmd.Wait after the direct child exits while a
	// descendant still holds an inherited stdout or stderr pipe.
	WaitDelay = 3 * time.Second
)

// Process is a started command and its Heimdallm-owned process group. A
// Process must be completed with Wait. Terminate and Kill are safe to call
// concurrently with Wait and with each other, which lets the daemon shutdown
// path sweep executions without racing their normal completion.
type Process struct {
	cmd   *exec.Cmd
	group *ownedGroup
}

// ownedGroup keeps the sentinel around until the final group signal has been
// delivered. All lifecycle transitions and signals are serialized by mu. That
// is more than bookkeeping: after finish reaps the sentinel, a late
// Terminate/Kill call must perform no syscall at all because the numeric PGID
// is then free to be reused.
type ownedGroup struct {
	pgid       int
	sentinel   *exec.Cmd
	hold       *os.File
	mu         sync.Mutex
	termSent   bool
	killSent   bool
	finalizing bool
	closed     bool
	killDone   chan struct{}
	waitOnce   sync.Once
	sentinelCh chan struct{}

	termErr error
	killErr error
}

// Start launches a sentinel, makes cmd join the sentinel's process group, and
// starts cmd. cmd must have been created with exec.CommandContext: Start owns
// cmd.Cancel and cmd.WaitDelay, and os/exec rejects a Cancel hook on a command
// without an associated context.
func Start(cmd *exec.Cmd) (*Process, error) {
	group, err := startOwnedGroup()
	if err != nil {
		return nil, err
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = group.pgid

	process := &Process{cmd: cmd, group: group}
	cmd.Cancel = process.Terminate
	cmd.WaitDelay = WaitDelay

	if err := cmd.Start(); err != nil {
		group.abort()
		return nil, err
	}
	return process, nil
}

// startOwnedGroup starts a shell blocked on an inherited pipe. The shell is a
// tiny, portable sentinel available on both supported Unix platforms; using a
// pipe avoids a busy loop or a timer that could expire during a long AI run.
// The parent alone retains the write end until reapSentinel, so EOF is also a
// process-lifetime signal: if the daemon crashes or is SIGKILLed before normal
// cleanup, the sentinel terminates only its own reserved group and escalates to
// SIGKILL after TermGrace. This closes the one path TerminateAll cannot run.
func startOwnedGroup() (*ownedGroup, error) {
	readEnd, hold, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("procgroup: create sentinel pipe: %w", err)
	}

	const sentinelScript = `
trap '' TERM
if IFS= read -r _ <&3; then
  exit 0
fi
kill -TERM "-$$" 2>/dev/null || true
sleep "$1"
kill -KILL "-$$" 2>/dev/null || true
`
	sentinel := exec.Command( //nolint:noctx // lifetime is owned explicitly below
		"/bin/sh",
		"-c",
		sentinelScript,
		"heimdallm-procgroup-sentinel",
		fmt.Sprintf("%.3f", TermGrace.Seconds()),
	)
	sentinel.ExtraFiles = []*os.File{readEnd}
	sentinel.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sentinel.Start(); err != nil {
		_ = readEnd.Close()
		_ = hold.Close()
		return nil, fmt.Errorf("procgroup: start process-group sentinel: %w", err)
	}
	_ = readEnd.Close()

	return &ownedGroup{
		pgid:       sentinel.Process.Pid,
		sentinel:   sentinel,
		hold:       hold,
		killDone:   make(chan struct{}),
		sentinelCh: make(chan struct{}),
	}, nil
}

// ID returns the reserved process-group ID.
func (p *Process) ID() int {
	if p == nil || p.group == nil {
		return 0
	}
	return p.group.pgid
}

// Wait waits for cmd and then fully drains its owned group before returning.
// Explicit cancellation and ErrWaitDelay receive TERM plus TermGrace; an
// ordinary completion immediately kills any residual sentinel/helper. The
// latter avoids adding one second to every successful git command while still
// covering descendants that detached their output pipes.
func (p *Process) Wait() error {
	err := p.cmd.Wait()
	p.group.finish(errors.Is(err, exec.ErrWaitDelay))
	return err
}

// Terminate sends SIGTERM once and schedules SIGKILL after TermGrace. Because
// the sentinel is not reaped until Wait finishes, the scheduled signal cannot
// hit a recycled PGID even when the direct command exits during the grace
// period.
func (p *Process) Terminate() error {
	if p == nil || p.group == nil {
		return os.ErrProcessDone
	}
	return p.group.terminate()
}

// Kill immediately escalates the owned group to SIGKILL. It is safe after Wait
// has returned: the closed lifecycle state prevents any syscall after the
// sentinel was reaped.
func (p *Process) Kill() error {
	if p == nil || p.group == nil {
		return os.ErrProcessDone
	}
	return p.group.kill()
}

func (g *ownedGroup) terminate() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.termSent {
		return g.termErr
	}
	if g.finalizing || g.closed {
		return os.ErrProcessDone
	}
	g.startTerminationLocked()
	return g.termErr
}

func (g *ownedGroup) kill() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.killSent {
		return g.killErr
	}
	if g.closed {
		return os.ErrProcessDone
	}
	g.finalizing = true
	g.killLocked()
	return g.killErr
}

func (g *ownedGroup) startTerminationLocked() {
	g.termSent = true
	g.termErr = sendGroupSignal(g.pgid, syscall.SIGTERM)
	go func() {
		timer := time.NewTimer(TermGrace)
		defer timer.Stop()
		<-timer.C
		_ = g.kill()
	}()
}

func (g *ownedGroup) killLocked() {
	g.killSent = true
	g.killErr = sendGroupSignal(g.pgid, syscall.SIGKILL)
	close(g.killDone)
}

func (g *ownedGroup) finish(useTermGrace bool) {
	g.mu.Lock()
	switch {
	case g.killSent:
		// An external shutdown already escalated the group.
	case g.termSent:
		// Cancellation already started the grace timer.
	case useTermGrace:
		// The direct child exited cleanly but an inherited pipe proves that a
		// descendant remains. Give it the same graceful shutdown as cancel.
		g.startTerminationLocked()
	default:
		// Normal completion: the direct child is already gone. Kill the
		// sentinel and any detached helper immediately so ordinary git calls
		// do not all pay TermGrace.
		g.finalizing = true
		g.killLocked()
	}
	done := g.killDone
	g.mu.Unlock()

	<-done
	g.reapSentinel()
}

// abort is used when the real command never started. No untrusted process ever
// joined the group, so there is no reason to spend the TERM grace period.
func (g *ownedGroup) abort() {
	g.mu.Lock()
	if !g.killSent {
		g.finalizing = true
		g.killLocked()
	}
	done := g.killDone
	g.mu.Unlock()
	<-done
	g.reapSentinel()
}

func (g *ownedGroup) reapSentinel() {
	g.waitOnce.Do(func() {
		// Keep mu locked across Wait. sentinel.Wait is the exact instant at
		// which the numeric PID/PGID becomes reusable; a concurrent late
		// Terminate/Kill must observe closed before it can issue a syscall.
		g.mu.Lock()
		_ = g.hold.Close()
		err := g.sentinel.Wait()
		g.closed = true
		g.mu.Unlock()
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				slog.Debug("procgroup: wait for sentinel", "pgid", g.pgid, "err", err)
			}
		}
		close(g.sentinelCh)
	})
	<-g.sentinelCh
}

// Run starts cmd, waits for it, and guarantees that no live member of its
// Heimdallm-owned process group remains when it returns. A clean direct-child
// exit whose descendant held an output pipe still reports success; the output
// is already buffered and the descendant has been terminated before return.
func Run(cmd *exec.Cmd) error {
	process, err := Start(cmd)
	if err != nil {
		return err
	}
	err = process.Wait()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
		slog.Warn("procgroup: command exited 0 but a descendant held the output pipes; "+
			"reporting success after cleaning its process group",
			"path", cmd.Path, "pgid", process.ID())
		return nil
	}
	return err
}

// signalGroup signals pgid and translates ESRCH to os.ErrProcessDone, the
// value os/exec accepts from a Cancel hook when the process is already gone.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-pgid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// sendGroupSignal is a test seam for proving that closed groups reject late
// signals without entering the kernel. Production always points at signalGroup.
var sendGroupSignal = signalGroup
