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
// and tolerates ErrWaitDelay when the payload is already complete). That was
// deliberately left in place rather than folded in here — see #614.
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
	termGrace = 2 * time.Second
	// waitDelay bounds Wait once the direct child has exited but a descendant
	// still holds the inherited stdout/stderr pipes. Without it, Wait blocks
	// until every copy of those descriptors is closed, which lets a stalled
	// grandchild hang the caller indefinitely.
	waitDelay = 2 * time.Second
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
	err := cmd.Wait()
	reaped.Store(true) // before closing runDone: no signal may follow the reap
	close(runDone)
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
