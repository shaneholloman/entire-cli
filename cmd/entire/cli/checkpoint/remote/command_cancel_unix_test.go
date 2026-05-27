//go:build unix

package remote

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestKillProcessGroupOnCancel_SetsSetpgidAndCancel(t *testing.T) {
	t.Parallel()

	cmd := newCommand(context.Background(), "push", "origin", "main")

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("Setpgid = false; want true so the whole process group can be killed")
	}
	if cmd.Cancel == nil {
		t.Fatal("Cancel = nil; want a group-kill handler")
	}
}

// TestTerminateOnCancel_KillsProcessGroup proves the fix end-to-end: a child that
// backgrounds a grandchild which inherits (and holds open) the output pipe must
// still be terminated when the context is cancelled. Without group-kill, the
// backgrounded `sleep` keeps the pipe open and CombinedOutput blocks for the full
// 60s; with it, the whole group dies and CombinedOutput returns promptly.
func TestTerminateOnCancel_KillsProcessGroup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// `sleep 60 &` backgrounds a grandchild that inherits and holds stdout; `wait`
	// keeps the shell (the group leader) alive so only a group-wide kill ends both.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 60 & wait")
	terminateOnCancel(cmd)

	done := make(chan error, 1)
	go func() {
		_, err := cmd.CombinedOutput()
		done <- err
	}()

	// Group-kill closes the inherited pipe on cancellation, so CombinedOutput
	// returns well under the 60s sleep. Without it, the backgrounded grandchild
	// keeps the pipe open and this would block for the full minute.
	//
	// The deadline must stay strictly below killWaitDelay: that WaitDelay backstop
	// would itself force the pipe closed once it elapses, so a deadline >=
	// killWaitDelay would let this test pass even with group-kill removed, silently
	// turning it into a no-op. Halving keeps it comfortably inside that window.
	select {
	case <-done:
	case <-time.After(killWaitDelay / 2):
		t.Fatal("CombinedOutput did not return after cancellation; the pipe-holding grandchild outlived the group-kill")
	}
}

// The Cancel handler must return cleanly and actually terminate the process
// group leader (the running shell), not just succeed silently.
func TestKillProcessGroupOnCancel_TerminatesLeader(t *testing.T) {
	t.Parallel()

	// exec requires CommandContext when Cancel is non-nil; the context stays
	// open and we invoke Cancel directly to exercise the group-kill handler.
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "sleep 60 & wait")
	killProcessGroupOnCancel(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := cmd.Cancel(); err != nil {
		t.Fatalf("Cancel returned %v; want nil", err)
	}

	// The leader was SIGKILLed, so Wait reports a non-nil (signal: killed) error
	// rather than blocking on the 60s sleep.
	if err := cmd.Wait(); err == nil {
		t.Error("Wait returned nil; the killed shell should report a termination error")
	}
}
