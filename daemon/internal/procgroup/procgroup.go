// Package procgroup runs an *exec.Cmd in its own process group so that
// cancelling the command's context terminates the whole process tree rather
// than just the direct child.
//
// The problem it solves: exec.CommandContext's default cancellation kills only
// the process it started. Anything that process forked survives, gets reparented
// to PID 1, and — because nothing calls wait() on a process it did not spawn —
// becomes a permanent zombie once it exits. For `git` over SSH the forked child
// is `ssh`, which is how a container accumulated 13 [ssh] + 2 [git] zombies in
// 23h of uptime (theburrowhub/heimdallm#665). The same shape was already
// diagnosed for the AI CLI launchers in #614.
//
// Killing the group is only half of the fix. A descendant that is already
// orphaned still needs PID 1 to reap it, and the kernel does not do that
// automatically for PID 1 — hence the init process in docker/Dockerfile. This
// package stops the daemon from leaking live processes; the init stops the dead
// ones from piling up as zombies.
//
// Note for future readers: internal/executor carries a richer variant of this
// logic (it also registers groups so TerminateAll can reach agents at shutdown,
// and returns the collected payload alongside its ErrWaitDelay tolerance). That
// was deliberately left in place rather than folded in here — see #614. The two
// copies must agree on the timer ordering and on tolerating a clean exit whose
// descendant holds the pipes; both are called out at their definitions.
package procgroup

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// termGrace is how long the group gets to exit after SIGTERM before the
	// escalation to SIGKILL. Short on purpose: cancellation here means a
	// timeout has already elapsed, so the caller is past waiting politely.
	//
	// INVARIANT: termGrace must stay strictly — and comfortably — below
	// waitDelay. On cancellation os/exec arms its own WaitDelay timer at the
	// same moment Cancel fires, and when that timer expires os/exec kills only
	// the DIRECT child and lets Wait return. If the two timers were equal (or
	// termGrace were larger) the escalation goroutine would find `reaped`
	// already true, skip kill(-pgid, SIGKILL), and leave alive precisely the
	// descendant that ignored SIGTERM — reintroducing #665 on every timeout
	// where the group does not die on the first signal. internal/executor
	// encodes the same ordering as 1s vs 3s; keep both copies in step.
	termGrace = 1 * time.Second
	// waitDelay bounds Wait once the direct child has exited but a descendant
	// still holds the inherited stdout/stderr pipes. Without it, Wait blocks
	// until every copy of those descriptors is closed, which lets a stalled
	// grandchild hang the caller indefinitely. See the termGrace invariant.
	waitDelay = 3 * time.Second
)

// Run starts cmd in its own process group, waits for it, and on context
// cancellation signals the entire group (SIGTERM, then SIGKILL after
// termGrace).
//
// cmd MUST have been created with exec.CommandContext. Run installs a
// cmd.Cancel hook, and os/exec refuses to start a command that has one without
// an associated context — so misuse fails immediately at Start with an explicit
// error rather than silently losing the group kill. The requirement is
// semantic, not incidental: with no context there is nothing to cancel and
// nothing for this package to do.
//
// Run replaces cmd.Run and takes ownership of cmd.SysProcAttr.Setpgid,
// cmd.Cancel and cmd.WaitDelay. It deliberately wraps Start+Wait instead of
// exposing a "configure this cmd" helper: the escalation goroutine must be told
// when the child has been reaped, and a caller that forgot to do so would
// eventually signal a pgid the kernel had already recycled onto an unrelated
// process group.
//
// A clean exit whose descendant still holds the inherited pipes is reported as
// success, not as exec.ErrWaitDelay — see the comment at that branch.
//
// The residual race is the same one documented in #614 and is narrowed, not
// closed: `reaped` is set the instant Wait returns and is checked immediately
// before the kill, so the window is the gap between those two statements.
// Closing it entirely needs a wait-without-reap (wait4(WNOWAIT) or pidfd).
func Run(cmd *exec.Cmd) error {
	// Setpgid makes the child the leader of a new group whose ID is its own
	// PID, which is what lets a single negated PID address every descendant
	// that has not created a group of its own.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	runDone := make(chan struct{})
	var reaped atomic.Bool

	cmd.Cancel = func() error {
		err := signalGroup(cmd, syscall.SIGTERM)
		go func() {
			select {
			case <-time.After(termGrace):
				if reaped.Load() {
					// The pgid may already have been handed to somebody else;
					// a late signal would hit an unrelated group.
					return
				}
				if killErr := signalGroup(cmd, syscall.SIGKILL); killErr != nil {
					slog.Debug("procgroup: group already gone before SIGKILL",
						"path", cmd.Path, "err", killErr)
				}
			case <-runDone:
			}
		}()
		return err
	}
	cmd.WaitDelay = waitDelay

	if err := cmd.Start(); err != nil {
		close(runDone)
		return err
	}
	pgid := cmd.Process.Pid // Setpgid makes the child its own group leader
	err := cmd.Wait()
	reaped.Store(true) // before closing runDone: no signal may follow the reap
	close(runDone)

	// WaitDelay also fires when the command itself exited cleanly but a
	// descendant still holds the inherited pipes. That is common for git, not
	// exotic: `git gc --auto` is spawned in the background after fetch/commit/
	// merge, and an SSH remote using ControlMaster/ControlPersist leaves the mux
	// master running on purpose. Returning ErrWaitDelay to the caller in that
	// case would report a `git push` that actually succeeded as a failure and
	// discard its output, so the pipeline would retry or fail an operation that
	// already applied.
	//
	// Everything the command itself wrote is already buffered — it wrote it
	// before exiting, and the copier drains concurrently — so the collected
	// output is complete and the exit status is authoritative. Swallow the
	// error and report success, matching internal/executor's handling of the
	// identical case.
	//
	// The descendant is NOT swept here: the child has been reaped, so its pgid
	// may already belong to another group and a signal would be aimed at
	// strangers. Log its identity instead — this is the one place with positive
	// evidence of a live leaked process, so it should be traceable while #614
	// is open. With Setpgid the group ID is the leader's PID, so one number
	// identifies both.
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
		slog.Warn("procgroup: command exited 0 but a descendant held the output pipes; "+
			"reporting success and leaving the descendant running",
			"path", cmd.Path, "pgid", pgid)
		return nil
	}
	return err
}

// signalGroup sends sig to the whole group led by cmd's child, reporting an
// already-exited group as os.ErrProcessDone.
//
// That translation is required, not cosmetic: os/exec only tolerates a
// cmd.Cancel error that is os.ErrProcessDone, so a raw ESRCH would surface out
// of Wait and disguise a normal cancellation as a bogus "no such process"
// failure on the caller's error path.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
