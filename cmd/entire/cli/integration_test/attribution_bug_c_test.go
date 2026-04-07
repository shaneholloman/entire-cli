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
	"github.com/go-git/go-git/v6/plumbing/object"
)

// TestManualCommit_Attribution_MultiSessionCombinedSummary demonstrates Bug C:
// when two sessions condense into the same checkpoint, session-level attribution
// is preserved, but the root checkpoint metadata does not expose a combined
// attribution view for the checkpoint as a whole.
func TestManualCommit_Attribution_MultiSessionCombinedSummary(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	defer env.Cleanup()

	env.InitRepo()
	env.WriteFile("session1.go", "package main\n")
	env.WriteFile("session2.go", "package main\n")
	env.GitAdd("session1.go")
	env.GitAdd("session2.go")
	env.GitCommit("Initial commit")

	env.InitEntire()

	// Session 1: mixed agent + user contribution.
	session1 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session1.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit session1 prompt1 failed: %v", err)
	}

	session1Checkpoint1 := "package main\n\nfunc agentOne() int {\n\treturn 1\n}\n"
	env.WriteFile("session1.go", session1Checkpoint1)
	session1.CreateTranscript(
		"Add first agent function to session1.go",
		[]FileChange{{Path: "session1.go", Content: session1Checkpoint1}},
	)
	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop session1 checkpoint1 failed: %v", err)
	}

	// Human edits between prompts for session 1.
	session1WithHumanEdits := session1Checkpoint1 +
		"// human note 1\n" +
		"// human note 2\n"
	env.WriteFile("session1.go", session1WithHumanEdits)

	if err := env.SimulateUserPromptSubmit(session1.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit session1 prompt2 failed: %v", err)
	}

	session1Final := session1WithHumanEdits + "\nfunc agentTwo() int {\n\treturn 2\n}\n"
	env.WriteFile("session1.go", session1Final)
	session1.CreateTranscript(
		"Add second agent function to session1.go",
		[]FileChange{{Path: "session1.go", Content: session1Final}},
	)
	if err := env.SimulateStop(session1.ID, session1.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop session1 checkpoint2 failed: %v", err)
	}

	// Session 2: agent-only contribution.
	session2 := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session2.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit session2 failed: %v", err)
	}

	session2Final := "package main\n\nfunc isolatedAgentWork() int {\n\treturn 99\n}\n"
	env.WriteFile("session2.go", session2Final)
	session2.CreateTranscript(
		"Add isolated agent work to session2.go",
		[]FileChange{{Path: "session2.go", Content: session2Final}},
	)
	if err := env.SimulateStop(session2.ID, session2.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop session2 failed: %v", err)
	}

	// Single user commit condenses both sessions into one checkpoint.
	env.GitCommitWithShadowHooks("Condense both sessions", "session1.go", "session2.go")

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		t.Fatalf("failed to open repo: %v", err)
	}

	headHash := env.GetHeadHash()
	headCommit, err := repo.CommitObject(plumbing.NewHash(headHash))
	if err != nil {
		t.Fatalf("failed to get head commit: %v", err)
	}

	checkpointID, found := trailers.ParseCheckpoint(headCommit.Message)
	if !found {
		t.Fatal("commit should have Entire-Checkpoint trailer")
	}

	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get metadata branch: %v", err)
	}
	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("failed to get metadata branch commit: %v", err)
	}
	sessionsTree, err := sessionsCommit.Tree()
	if err != nil {
		t.Fatalf("failed to get metadata tree: %v", err)
	}

	summaryPath := CheckpointSummaryPath(checkpointID.String())
	summaryContent, err := readFileFromTree(sessionsTree, summaryPath)
	if err != nil {
		t.Fatalf("failed to read checkpoint summary: %v", err)
	}

	var summary struct {
		Sessions            []checkpoint.SessionFilePaths  `json:"sessions"`
		CombinedAttribution *checkpoint.InitialAttribution `json:"combined_attribution"`
	}
	if err := json.Unmarshal(summaryContent, &summary); err != nil {
		t.Fatalf("failed to parse checkpoint summary: %v", err)
	}

	if len(summary.Sessions) != 2 {
		t.Fatalf("expected 2 sessions in checkpoint summary, got %d", len(summary.Sessions))
	}

	var sawHumanContribution bool
	var sawAgentOnlySession bool

	for i := range summary.Sessions {
		metadataPath := fmt.Sprintf("%s/%d/%s", checkpointID.Path(), i, paths.MetadataFileName)
		metadataContent, readErr := readFileFromTree(sessionsTree, metadataPath)
		if readErr != nil {
			t.Fatalf("failed to read session %d metadata: %v", i, readErr)
		}

		var metadata checkpoint.CommittedMetadata
		if err := json.Unmarshal(metadataContent, &metadata); err != nil {
			t.Fatalf("failed to parse session %d metadata: %v", i, err)
		}
		if metadata.InitialAttribution == nil {
			t.Fatalf("session %d missing initial attribution", i)
		}

		if metadata.InitialAttribution.HumanAdded > 0 || metadata.InitialAttribution.HumanModified > 0 {
			sawHumanContribution = true
		}
		if metadata.InitialAttribution.HumanAdded == 0 && metadata.InitialAttribution.HumanModified == 0 {
			sawAgentOnlySession = true
		}
	}

	if !sawHumanContribution {
		t.Fatal("expected one session to retain human contribution in session metadata")
	}
	if !sawAgentOnlySession {
		t.Fatal("expected one session to remain agent-only in session metadata")
	}

	if summary.CombinedAttribution == nil {
		t.Fatal("combined_attribution missing from root checkpoint metadata")
	}
	if summary.CombinedAttribution.HumanAdded == 0 && summary.CombinedAttribution.HumanModified == 0 {
		t.Fatal("combined_attribution should preserve human contribution from the earlier session")
	}
	if summary.CombinedAttribution.AgentPercentage >= 100 {
		t.Fatalf("combined_attribution agent percentage = %.1f, want < 100 for mixed-session checkpoint",
			summary.CombinedAttribution.AgentPercentage)
	}
}

func readFileFromTree(tree *object.Tree, path string) ([]byte, error) {
	file, err := tree.File(path)
	if err != nil {
		return nil, err
	}
	content, err := file.Contents()
	if err != nil {
		return nil, err
	}
	return []byte(content), nil
}
