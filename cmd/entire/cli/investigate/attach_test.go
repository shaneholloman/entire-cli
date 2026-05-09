package investigate

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// captureAttachCall records the args passed to AttachDeps.Attach so
// tests can assert on them without touching real session state.
type captureAttachCall struct {
	called    bool
	sessionID string
	runID     string
	round     int
	turn      int
	topic     string
	prompt    string
	err       error
}

func (c *captureAttachCall) attach(_ context.Context, sessionID, runID string, round, turn int, topic, prompt string) error {
	c.called = true
	c.sessionID = sessionID
	c.runID = runID
	c.round = round
	c.turn = turn
	c.topic = topic
	c.prompt = prompt
	return c.err
}

// TestNewAttachCommand_RequiresSessionID verifies that running attach
// with no positional args returns the cobra arg error (cobra.ExactArgs(1)).
func TestNewAttachCommand_RequiresSessionID(t *testing.T) {
	t.Parallel()

	capt := &captureAttachCall{}
	cmd := NewAttachCommand(AttachDeps{Attach: capt.attach})
	cmd.SetArgs([]string{})

	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from missing positional <session-id>, got nil")
	}
	if capt.called {
		t.Errorf("expected Attach not to be called when args are missing, but it was")
	}
}

// TestNewAttachCommand_PassesArgsToDeps verifies that flag values and
// the positional session-id reach AttachDeps.Attach unchanged.
func TestNewAttachCommand_PassesArgsToDeps(t *testing.T) {
	t.Parallel()

	capt := &captureAttachCall{}
	cmd := NewAttachCommand(AttachDeps{Attach: capt.attach})
	cmd.SetArgs([]string{
		"sess-001",
		"--run-id", "0123456789ab",
		"--round", "2",
		"--turn", "5",
		"--topic", "foo",
		"--prompt", "investigate flake X",
	})

	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !capt.called {
		t.Fatal("expected Attach to be called")
	}
	if capt.sessionID != "sess-001" {
		t.Errorf("sessionID = %q, want %q", capt.sessionID, "sess-001")
	}
	if capt.runID != "0123456789ab" {
		t.Errorf("runID = %q, want %q", capt.runID, "0123456789ab")
	}
	if capt.round != 2 {
		t.Errorf("round = %d, want 2", capt.round)
	}
	if capt.turn != 5 {
		t.Errorf("turn = %d, want 5", capt.turn)
	}
	if capt.topic != "foo" {
		t.Errorf("topic = %q, want %q", capt.topic, "foo")
	}
	if capt.prompt != "investigate flake X" {
		t.Errorf("prompt = %q, want %q", capt.prompt, "investigate flake X")
	}
	if !strings.Contains(out.String(), "Tagged session sess-001") {
		t.Errorf("expected success message, got: %s", out.String())
	}
}

// TestNewAttachCommand_RejectsEmptyRunIDAndTopic verifies that omitting
// both --run-id and --topic surfaces an error mentioning both flags.
func TestNewAttachCommand_RejectsEmptyRunIDAndTopic(t *testing.T) {
	t.Parallel()

	capt := &captureAttachCall{}
	cmd := NewAttachCommand(AttachDeps{Attach: capt.attach})
	cmd.SetArgs([]string{"sess-empty"})

	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if capt.called {
		t.Error("expected Attach not to be called when validation fails")
	}
	combined := err.Error() + out.String()
	for _, want := range []string{"--run-id", "--topic"} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected error to mention %q, got: %s", want, combined)
		}
	}
}

// TestNewAttachCommand_RejectsBadRunID verifies that a non-hex run ID
// surfaces a validation error and Attach is never called.
func TestNewAttachCommand_RejectsBadRunID(t *testing.T) {
	t.Parallel()

	capt := &captureAttachCall{}
	cmd := NewAttachCommand(AttachDeps{Attach: capt.attach})
	cmd.SetArgs([]string{"sess-bad-runid", "--run-id", "BADHEX"})

	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected validation error for malformed run id, got nil")
	}
	if capt.called {
		t.Error("expected Attach not to be called when run id is invalid")
	}
	if !strings.Contains(err.Error(), "run-id") {
		t.Errorf("expected error to mention run-id, got: %v", err)
	}
}

// TestNewAttachCommand_SurfacesAttachError verifies that errors from the
// injected Attach reach the caller (cobra returns them).
func TestNewAttachCommand_SurfacesAttachError(t *testing.T) {
	t.Parallel()

	capt := &captureAttachCall{err: errors.New("boom")}
	cmd := NewAttachCommand(AttachDeps{Attach: capt.attach})
	cmd.SetArgs([]string{"sess-x", "--topic", "anything"})

	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error to surface, got %v", err)
	}
}
