//go:build integration

package integration

import (
	"testing"
)

// TestAttribution_CrossSessionInflation reproduces the exact scenario from manual testing:
//
//	claude -p "create file docs/blue.md 3 lines" → Session 1
//	claude -p "create file docs/red.md 3 lines"  → Session 2
//	claude -p "commit all"                        → Session 3 (commits)
//
// Expected: agent=7 (3+4 lines), human=0, total=7, percentage=100%
// Bug:      agent=14, human=7, total=21, percentage=66.67%
//
// Root causes:
//  1. Per-session attribution counts OTHER sessions' files as non-agent human work
//  2. Session 3 (empty filesTouched) gets ALL committed files via fallback
//  3. updateCombinedAttributionForCheckpoint naively sums inflated per-session values
func TestAttribution_CrossSessionInflation(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	env.InitRepo()
	env.WriteFile("README.md", "# Test repo\n")
	env.GitAdd("README.md")
	env.GitCommit("Initial commit")

	env.InitEntire()

	// =============================================
	// Session 1: Agent creates docs/blue.md (3 lines)
	// Simulates: claude -p "create file docs/blue.md 3 lines of random content"
	// =============================================
	session1 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session1.ID); err != nil {
		t.Fatalf("Session 1 UserPromptSubmit: %v", err)
	}

	blueContent := "The sky is blue today.\nClouds drift by slowly.\nSunshine fills the air.\n"
	env.WriteFile("docs/blue.md", blueContent)

	session1.CreateTranscript(
		"create file docs/blue.md 3 lines of random content",
		[]FileChange{{Path: "docs/blue.md", Content: blueContent}},
	)
	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("Session 1 Stop: %v", err)
	}
	// Session 1 ends (claude -p exits after one prompt)
	if err := env.SimulateSessionEnd(session1.ID); err != nil {
		t.Fatalf("Session 1 SessionEnd: %v", err)
	}

	// =============================================
	// Session 2: Agent creates docs/red.md (4 lines)
	// Simulates: claude -p "create file docs/red.md 3 lines of random content"
	// (agent generated 4 lines — common with LLMs)
	// =============================================
	session2 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session2.ID); err != nil {
		t.Fatalf("Session 2 UserPromptSubmit: %v", err)
	}

	redContent := "Roses are red today.\nViolets are blue.\nSugar is sweet.\nAnd so are you.\n"
	env.WriteFile("docs/red.md", redContent)

	session2.CreateTranscript(
		"create file docs/red.md 3 lines of random content",
		[]FileChange{{Path: "docs/red.md", Content: redContent}},
	)
	if err := env.SimulateStop(session2.ID, session2.TranscriptPath); err != nil {
		t.Fatalf("Session 2 Stop: %v", err)
	}
	// Session 2 ends
	if err := env.SimulateSessionEnd(session2.ID); err != nil {
		t.Fatalf("Session 2 SessionEnd: %v", err)
	}

	// =============================================
	// Session 3: Agent commits all files
	// Simulates: claude -p "commit all"
	// Agent doesn't create files, just runs git add + git commit
	// =============================================
	session3 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session3.ID); err != nil {
		t.Fatalf("Session 3 UserPromptSubmit: %v", err)
	}

	// Session 3's agent doesn't create files — it just commits.
	// No Stop/SaveStep because the commit happens before the agent finishes.
	// The commit triggers PostCommit which condenses all sessions.

	// Commit both files (this is what the agent does)
	env.GitCommitWithShadowHooksAsAgent("Add docs", "docs/blue.md", "docs/red.md")

	// Session 3 ends after committing
	if err := env.SimulateStop(session3.ID, session3.TranscriptPath); err != nil {
		// Session 3 may not have a transcript yet — that's OK
		t.Logf("Session 3 Stop (expected to possibly fail): %v", err)
	}

	// =============================================
	// Verify per-session attribution
	// Each session should show 100% agent for its own files, with no cross-session inflation.
	// =============================================

	// Session 1 (blue.md): 3 agent lines, 0 human
	attr1 := getAttributionForHeadSession(t, env, 0)
	t.Logf("Session 1: agent=%d, human_added=%d, total=%d, pct=%.1f%%",
		attr1.AgentLines, attr1.HumanAdded, attr1.TotalCommitted, attr1.AgentPercentage)
	if attr1.HumanAdded != 0 {
		t.Errorf("Session 1 HumanAdded = %d, want 0 (red.md should not count as human)", attr1.HumanAdded)
	}
	if attr1.AgentLines != 3 {
		t.Errorf("Session 1 AgentLines = %d, want 3 (blue.md)", attr1.AgentLines)
	}
	if attr1.AgentPercentage < 99.9 {
		t.Errorf("Session 1 AgentPercentage = %.1f%%, want 100%%", attr1.AgentPercentage)
	}

	// Session 2 (red.md): 4 agent lines, 0 human
	attr2 := getAttributionForHeadSession(t, env, 1)
	t.Logf("Session 2: agent=%d, human_added=%d, total=%d, pct=%.1f%%",
		attr2.AgentLines, attr2.HumanAdded, attr2.TotalCommitted, attr2.AgentPercentage)
	if attr2.HumanAdded != 0 {
		t.Errorf("Session 2 HumanAdded = %d, want 0 (blue.md should not count as human)", attr2.HumanAdded)
	}
	if attr2.AgentLines != 4 {
		t.Errorf("Session 2 AgentLines = %d, want 4 (red.md)", attr2.AgentLines)
	}
	if attr2.AgentPercentage < 99.9 {
		t.Errorf("Session 2 AgentPercentage = %.1f%%, want 100%%", attr2.AgentPercentage)
	}
}
