//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/trailers"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// TestAttribution_DiagnosePhantomLines inspects PromptAttribution values at each
// step of a multi-turn session to find where phantom user lines could be introduced.
//
// This test mirrors the real scenario: 4-turn session, all agent work, user commits.
// After each hook, we read the session state and log the PA values.
func TestAttribution_DiagnosePhantomLines(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	env.InitRepo()

	// Base: two files
	env.WriteFile("hooks.go", "package strategy\n\nfunc warn() {}\n")
	env.WriteFile("test.go", "package strategy\n\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\nfunc TestC(t *testing.T) {}\n")
	env.GitAdd("hooks.go")
	env.GitAdd("test.go")
	env.GitCommit("Initial commit")

	env.InitEntire()

	session := env.NewSession()

	// ========================================
	// TURN 1: Agent modifies hooks.go
	// ========================================
	t.Log("=== TURN 1 ===")
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("Turn 1 UserPromptSubmit failed: %v", err)
	}

	// Check PA after first prompt
	state1, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after turn 1 prompt: %v", err)
	}
	if state1.PendingPromptAttribution != nil {
		t.Logf("Turn 1 PendingPA: checkpoint=%d, user_added=%d, user_removed=%d, per_file=%v",
			state1.PendingPromptAttribution.CheckpointNumber,
			state1.PendingPromptAttribution.UserLinesAdded,
			state1.PendingPromptAttribution.UserLinesRemoved,
			state1.PendingPromptAttribution.UserAddedPerFile)
	} else {
		t.Log("Turn 1 PendingPA: nil")
	}
	t.Logf("Turn 1 PromptAttributions count: %d", len(state1.PromptAttributions))

	// Agent adds a comment and a function to hooks.go
	hooksV1 := "package strategy\n\n// stderrWriter for testing\nvar stderrWriter = os.Stderr\n\nfunc warn() {}\n"
	env.WriteFile("hooks.go", hooksV1)

	session.CreateTranscript(
		"Add stderrWriter",
		[]FileChange{{Path: "hooks.go", Content: hooksV1}},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("Turn 1 Stop failed: %v", err)
	}

	// Check PA after SaveStep
	state1b, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after turn 1 stop: %v", err)
	}
	t.Logf("Turn 1 after stop: PromptAttributions=%d, PendingPA=%v",
		len(state1b.PromptAttributions), state1b.PendingPromptAttribution)
	for i, pa := range state1b.PromptAttributions {
		t.Logf("  PA[%d]: checkpoint=%d, user_added=%d, user_removed=%d, per_file=%v",
			i, pa.CheckpointNumber, pa.UserLinesAdded, pa.UserLinesRemoved, pa.UserAddedPerFile)
	}

	// ========================================
	// TURN 2: Agent refactors test.go (replace map with slice — like the real commit)
	// ========================================
	t.Log("=== TURN 2 ===")
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("Turn 2 UserPromptSubmit failed: %v", err)
	}

	state2, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after turn 2 prompt: %v", err)
	}
	if state2.PendingPromptAttribution != nil {
		t.Logf("Turn 2 PendingPA: checkpoint=%d, user_added=%d, user_removed=%d, per_file=%v",
			state2.PendingPromptAttribution.CheckpointNumber,
			state2.PendingPromptAttribution.UserLinesAdded,
			state2.PendingPromptAttribution.UserLinesRemoved,
			state2.PendingPromptAttribution.UserAddedPerFile)
	} else {
		t.Log("Turn 2 PendingPA: nil")
	}

	// Agent rewrites test.go: removes 3 old test functions, adds 4 new ones
	testV1 := "package strategy\n\nfunc TestX(t *testing.T) {}\nfunc TestY(t *testing.T) {}\nfunc TestZ(t *testing.T) {}\nfunc TestW(t *testing.T) {}\n"
	env.WriteFile("test.go", testV1)

	session.CreateTranscript(
		"Refactor tests",
		[]FileChange{{Path: "test.go", Content: testV1}},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("Turn 2 Stop failed: %v", err)
	}

	state2b, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after turn 2 stop: %v", err)
	}
	t.Logf("Turn 2 after stop: PromptAttributions=%d", len(state2b.PromptAttributions))
	for i, pa := range state2b.PromptAttributions {
		t.Logf("  PA[%d]: checkpoint=%d, user_added=%d, user_removed=%d, per_file=%v",
			i, pa.CheckpointNumber, pa.UserLinesAdded, pa.UserLinesRemoved, pa.UserAddedPerFile)
	}

	// ========================================
	// TURN 3: Agent adds a comment to hooks.go
	// ========================================
	t.Log("=== TURN 3 ===")
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("Turn 3 UserPromptSubmit failed: %v", err)
	}

	state3, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after turn 3 prompt: %v", err)
	}
	if state3.PendingPromptAttribution != nil {
		t.Logf("Turn 3 PendingPA: checkpoint=%d, user_added=%d, user_removed=%d, per_file=%v",
			state3.PendingPromptAttribution.CheckpointNumber,
			state3.PendingPromptAttribution.UserLinesAdded,
			state3.PendingPromptAttribution.UserLinesRemoved,
			state3.PendingPromptAttribution.UserAddedPerFile)
	}

	hooksV2 := "package strategy\n\n// stderrWriter for testing\n// Added in code review\nvar stderrWriter = os.Stderr\n\nfunc warn() {}\n"
	env.WriteFile("hooks.go", hooksV2)

	session.CreateTranscript(
		"Add comment to hooks",
		[]FileChange{{Path: "hooks.go", Content: hooksV2}},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("Turn 3 Stop failed: %v", err)
	}

	state3b, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after turn 3 stop: %v", err)
	}
	t.Logf("Turn 3 after stop: PromptAttributions=%d", len(state3b.PromptAttributions))
	for i, pa := range state3b.PromptAttributions {
		t.Logf("  PA[%d]: checkpoint=%d, user_added=%d, user_removed=%d, per_file=%v",
			i, pa.CheckpointNumber, pa.UserLinesAdded, pa.UserLinesRemoved, pa.UserAddedPerFile)
	}

	// ========================================
	// TURN 4: Agent makes final tweaks to test.go
	// ========================================
	t.Log("=== TURN 4 ===")
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("Turn 4 UserPromptSubmit failed: %v", err)
	}

	state4, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after turn 4 prompt: %v", err)
	}
	if state4.PendingPromptAttribution != nil {
		t.Logf("Turn 4 PendingPA: checkpoint=%d, user_added=%d, user_removed=%d, per_file=%v",
			state4.PendingPromptAttribution.CheckpointNumber,
			state4.PendingPromptAttribution.UserLinesAdded,
			state4.PendingPromptAttribution.UserLinesRemoved,
			state4.PendingPromptAttribution.UserAddedPerFile)
	}

	testV2 := "package strategy\n\nfunc TestX(t *testing.T) { t.Log(\"x\") }\nfunc TestY(t *testing.T) {}\nfunc TestZ(t *testing.T) {}\nfunc TestW(t *testing.T) {}\n"
	env.WriteFile("test.go", testV2)

	session.CreateTranscript(
		"Final test tweaks",
		[]FileChange{{Path: "test.go", Content: testV2}},
	)
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("Turn 4 Stop failed: %v", err)
	}

	state4b, err := env.GetSessionState(session.ID)
	if err != nil {
		t.Fatalf("GetSessionState after turn 4 stop: %v", err)
	}
	t.Logf("Turn 4 after stop: PromptAttributions=%d", len(state4b.PromptAttributions))
	for i, pa := range state4b.PromptAttributions {
		t.Logf("  PA[%d]: checkpoint=%d, user_added=%d, user_removed=%d, per_file=%v",
			i, pa.CheckpointNumber, pa.UserLinesAdded, pa.UserLinesRemoved, pa.UserAddedPerFile)
	}

	// ========================================
	// TOTAL accumulated before commit
	// ========================================
	var totalUserAdded, totalUserRemoved int
	for _, pa := range state4b.PromptAttributions {
		totalUserAdded += pa.UserLinesAdded
		totalUserRemoved += pa.UserLinesRemoved
	}
	t.Logf("=== TOTAL before commit: user_added=%d, user_removed=%d ===", totalUserAdded, totalUserRemoved)

	if totalUserAdded > 0 {
		t.Errorf("PHANTOM LINES: total user_added=%d across all PAs, but no user edits were made", totalUserAdded)
	}

	// ========================================
	// COMMIT
	// ========================================
	env.GitCommitWithShadowHooks("All agent work", "hooks.go", "test.go")

	attr := getAttributionForHead(t, env)
	t.Logf("Final attribution: agent=%d, human_added=%d, human_modified=%d, human_removed=%d, total=%d, percentage=%.1f%%",
		attr.AgentLines, attr.HumanAdded, attr.HumanModified, attr.HumanRemoved,
		attr.TotalCommitted, attr.AgentPercentage)

	if attr.HumanAdded != 0 {
		t.Errorf("HumanAdded = %d, want 0", attr.HumanAdded)
	}
	if attr.AgentPercentage < 99.9 {
		t.Errorf("AgentPercentage = %.1f%%, want 100%%", attr.AgentPercentage)
	}
}

// getAttributionForHead reads attribution from the most recent commit's checkpoint.
func getAttributionForHeadSession(t *testing.T, env *TestEnv, sessionIndex int) *checkpoint.InitialAttribution {
	t.Helper()

	headHash := env.GetHeadHash()
	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		t.Fatalf("failed to open repo: %v", err)
	}

	commitObj, err := repo.CommitObject(plumbing.NewHash(headHash))
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	checkpointID, found := trailers.ParseCheckpoint(commitObj.Message)
	if !found {
		t.Fatalf("Commit %s should have Entire-Checkpoint trailer", headHash[:7])
	}

	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("Failed to get entire/checkpoints/v1 branch: %v", err)
	}

	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("Failed to get sessions commit: %v", err)
	}

	sessionsTree, err := sessionsCommit.Tree()
	if err != nil {
		t.Fatalf("Failed to get sessions tree: %v", err)
	}

	metadataPath := fmt.Sprintf("%s/%d/%s", checkpointID.Path(), sessionIndex, paths.MetadataFileName)
	metadataFile, err := sessionsTree.File(metadataPath)
	if err != nil {
		t.Fatalf("Failed to read session metadata.json at path %s: %v", metadataPath, err)
	}

	metadataContent, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("Failed to read metadata content: %v", err)
	}

	var metadata checkpoint.CommittedMetadata
	if err := json.Unmarshal([]byte(metadataContent), &metadata); err != nil {
		t.Fatalf("Failed to parse metadata.json: %v", err)
	}

	if metadata.InitialAttribution == nil {
		t.Fatalf("InitialAttribution is nil for session %d", sessionIndex)
	}

	return metadata.InitialAttribution
}

func getAttributionForHead(t *testing.T, env *TestEnv) *checkpoint.InitialAttribution {
	t.Helper()

	headHash := env.GetHeadHash()
	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		t.Fatalf("failed to open repo: %v", err)
	}

	commitObj, err := repo.CommitObject(plumbing.NewHash(headHash))
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	checkpointID, found := trailers.ParseCheckpoint(commitObj.Message)
	if !found {
		t.Fatalf("Commit %s should have Entire-Checkpoint trailer", headHash[:7])
	}

	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("Failed to get entire/checkpoints/v1 branch: %v", err)
	}

	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("Failed to get sessions commit: %v", err)
	}

	sessionsTree, err := sessionsCommit.Tree()
	if err != nil {
		t.Fatalf("Failed to get sessions tree: %v", err)
	}

	metadataPath := SessionMetadataPath(checkpointID.String())
	metadataFile, err := sessionsTree.File(metadataPath)
	if err != nil {
		t.Fatalf("Failed to read session metadata.json at path %s: %v", metadataPath, err)
	}

	metadataContent, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("Failed to read metadata content: %v", err)
	}

	var metadata checkpoint.CommittedMetadata
	if err := json.Unmarshal([]byte(metadataContent), &metadata); err != nil {
		t.Fatalf("Failed to parse metadata.json: %v", err)
	}

	if metadata.InitialAttribution == nil {
		t.Fatal("InitialAttribution is nil")
	}

	return metadata.InitialAttribution
}
