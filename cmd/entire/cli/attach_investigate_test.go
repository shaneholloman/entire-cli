package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/session"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
)

// setupAttachSessionTestRepo initializes a minimal git repo + cwd so the
// session.NewStateStore call inside AttachSession can resolve the git
// common dir. AttachSession is state-only (no checkpoint creation, no
// transcript reads), so this is much lighter than setupAttachTestRepo.
func setupAttachSessionTestRepo(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "init.txt", "init")
	testutil.GitAdd(t, tmpDir, "init.txt")
	testutil.GitCommit(t, tmpDir, "init")
	t.Chdir(tmpDir)
	// Each test that calls Chdir invalidates the cached git common dir
	// resolved earlier in the process. Clear it so the new cwd is picked up.
	session.ClearGitCommonDirCache()
}

// seedSessionState writes a State for sessionID via the real StateStore
// so AttachSession can find and update it.
func seedSessionState(t *testing.T, state *session.State) {
	t.Helper()
	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
}

// loadSessionState loads the current persisted State for sessionID via the
// real StateStore.
func loadSessionState(t *testing.T, sessionID string) *session.State {
	t.Helper()
	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	state, err := store.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state == nil {
		t.Fatalf("expected state for %q, got nil", sessionID)
	}
	return state
}

// TestAttachSession_Investigate_Success verifies that AttachSession with
// AttachKindInvestigate marks an existing session and writes the
// investigate fields.
func TestAttachSession_Investigate_Success(t *testing.T) {
	setupAttachSessionTestRepo(t)

	sessionID := "sess-investigate-success"
	seedSessionState(t, &session.State{
		SessionID: sessionID,
		StartedAt: time.Now(),
	})

	err := AttachSession(context.Background(), AttachOptions{
		SessionID:         sessionID,
		Kind:              AttachKindInvestigate,
		InvestigateRunID:  "0123456789ab",
		InvestigateRound:  3,
		InvestigateTurn:   7,
		InvestigateTopic:  "flake-X",
		InvestigatePrompt: "investigate flake X",
	})
	if err != nil {
		t.Fatalf("AttachSession failed: %v", err)
	}

	state := loadSessionState(t, sessionID)
	if state.Kind != session.KindAgentInvestigate {
		t.Errorf("Kind = %q, want %q", state.Kind, session.KindAgentInvestigate)
	}
	if state.InvestigateRunID != "0123456789ab" {
		t.Errorf("InvestigateRunID = %q, want %q", state.InvestigateRunID, "0123456789ab")
	}
	if state.InvestigateRound != 3 {
		t.Errorf("InvestigateRound = %d, want 3", state.InvestigateRound)
	}
	if state.InvestigateTurn != 7 {
		t.Errorf("InvestigateTurn = %d, want 7", state.InvestigateTurn)
	}
	if state.InvestigateTopic != "flake-X" {
		t.Errorf("InvestigateTopic = %q, want %q", state.InvestigateTopic, "flake-X")
	}
	if state.InvestigatePrompt != "investigate flake X" {
		t.Errorf("InvestigatePrompt = %q, want %q", state.InvestigatePrompt, "investigate flake X")
	}
}

// TestAttachSession_Investigate_AlreadyTaggedReview verifies that a session
// already tagged as review cannot be retagged as investigate. The error
// message must mention both kinds.
func TestAttachSession_Investigate_AlreadyTaggedReview(t *testing.T) {
	setupAttachSessionTestRepo(t)

	sessionID := "sess-already-review"
	seedSessionState(t, &session.State{
		SessionID:    sessionID,
		StartedAt:    time.Now(),
		Kind:         session.KindAgentReview,
		ReviewSkills: []string{"skill-a"},
		ReviewPrompt: "review the diff",
	})

	err := AttachSession(context.Background(), AttachOptions{
		SessionID:        sessionID,
		Kind:             AttachKindInvestigate,
		InvestigateRunID: "0123456789ab",
	})
	if err == nil {
		t.Fatal("expected cross-kind retag to error, got nil")
	}
	for _, want := range []string{"already tagged as agent_review", "cannot retag as agent_investigate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}

	// Verify state was not mutated.
	state := loadSessionState(t, sessionID)
	if state.Kind != session.KindAgentReview {
		t.Errorf("Kind mutated to %q, want %q", state.Kind, session.KindAgentReview)
	}
	if state.InvestigateRunID != "" {
		t.Errorf("InvestigateRunID was set despite cross-kind error: %q", state.InvestigateRunID)
	}
}

// TestAttachSession_Investigate_Idempotent verifies that re-tagging the
// same session with AttachKindInvestigate updates the investigate fields
// without erroring.
func TestAttachSession_Investigate_Idempotent(t *testing.T) {
	setupAttachSessionTestRepo(t)

	sessionID := "sess-idempotent"
	seedSessionState(t, &session.State{
		SessionID: sessionID,
		StartedAt: time.Now(),
	})

	if err := AttachSession(context.Background(), AttachOptions{
		SessionID:         sessionID,
		Kind:              AttachKindInvestigate,
		InvestigateRunID:  "aaaaaaaaaaaa",
		InvestigateRound:  1,
		InvestigateTopic:  "topic-1",
		InvestigatePrompt: "prompt-1",
	}); err != nil {
		t.Fatalf("first AttachSession failed: %v", err)
	}

	// Second call: same Kind, different fields. Must succeed and overwrite.
	if err := AttachSession(context.Background(), AttachOptions{
		SessionID:         sessionID,
		Kind:              AttachKindInvestigate,
		InvestigateRunID:  "bbbbbbbbbbbb",
		InvestigateRound:  4,
		InvestigateTurn:   9,
		InvestigateTopic:  "topic-2",
		InvestigatePrompt: "prompt-2",
	}); err != nil {
		t.Fatalf("second AttachSession failed: %v", err)
	}

	state := loadSessionState(t, sessionID)
	if state.Kind != session.KindAgentInvestigate {
		t.Errorf("Kind = %q, want %q", state.Kind, session.KindAgentInvestigate)
	}
	if state.InvestigateRunID != "bbbbbbbbbbbb" {
		t.Errorf("InvestigateRunID = %q, want overwritten value", state.InvestigateRunID)
	}
	if state.InvestigateRound != 4 {
		t.Errorf("InvestigateRound = %d, want 4", state.InvestigateRound)
	}
	if state.InvestigateTurn != 9 {
		t.Errorf("InvestigateTurn = %d, want 9", state.InvestigateTurn)
	}
	if state.InvestigateTopic != "topic-2" {
		t.Errorf("InvestigateTopic = %q, want %q", state.InvestigateTopic, "topic-2")
	}
	if state.InvestigatePrompt != "prompt-2" {
		t.Errorf("InvestigatePrompt = %q, want %q", state.InvestigatePrompt, "prompt-2")
	}
}

// TestAttachSession_Review_StillWorks ensures the legacy
// attachReviewSession wrapper continues to behave correctly after the
// refactor: it tags the session as review with skills + prompt.
func TestAttachSession_Review_StillWorks(t *testing.T) {
	setupAttachSessionTestRepo(t)

	sessionID := "sess-review-wrapper"
	seedSessionState(t, &session.State{
		SessionID: sessionID,
		StartedAt: time.Now(),
	})

	skills := []string{"skill-a", "skill-b"}
	prompt := "review the diff"
	if err := attachReviewSession(context.Background(), sessionID, skills, prompt); err != nil {
		t.Fatalf("attachReviewSession failed: %v", err)
	}

	state := loadSessionState(t, sessionID)
	if state.Kind != session.KindAgentReview {
		t.Errorf("Kind = %q, want %q", state.Kind, session.KindAgentReview)
	}
	if !reflect.DeepEqual(state.ReviewSkills, skills) {
		t.Errorf("ReviewSkills = %v, want %v", state.ReviewSkills, skills)
	}
	if state.ReviewPrompt != prompt {
		t.Errorf("ReviewPrompt = %q, want %q", state.ReviewPrompt, prompt)
	}
}

// TestAttachSession_NotFound verifies that AttachSession on a missing
// session returns the expected "not found" error.
func TestAttachSession_NotFound(t *testing.T) {
	setupAttachSessionTestRepo(t)

	err := AttachSession(context.Background(), AttachOptions{
		SessionID:        "no-such-session",
		Kind:             AttachKindInvestigate,
		InvestigateTopic: "x",
	})
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}
