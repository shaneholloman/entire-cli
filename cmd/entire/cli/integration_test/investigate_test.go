//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/agent/spawn"
	"github.com/entireio/cli/cmd/entire/cli/execx"
	"github.com/entireio/cli/cmd/entire/cli/investigate"
	"github.com/entireio/cli/cmd/entire/cli/session"
)

// TestInvestigate_EnvVarAdoptionCondensesMetadataOnNextCommit pins the full
// investigate adoption pipeline: ENTIRE_INVESTIGATE_* env vars are set on the
// UserPromptSubmit hook subprocess (as `entire investigate` would do when
// spawning each per-turn agent), the lifecycle handler tags the session as
// agent_investigate, and the metadata is condensed into the checkpoint on the
// next git commit.
//
// Direct port of TestReview_EnvVarAdoptionCondensesReviewMetadataOnNextCommit,
// adapted for the investigate field set.
func TestInvestigate_EnvVarAdoptionCondensesMetadataOnNextCommit(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t)
	enableInvestigateAgent(t, env, "claude-code")

	const (
		runID    = "0123456789ab"
		round    = 1
		turn     = 1
		topic    = "how-does-x-work"
		userText = "Please investigate how X works on this branch."
		findings = "/tmp/investigate-findings.md"
		stateP   = "/tmp/investigate-state.json"
	)

	// Simulate the env vars that `entire investigate` sets on the spawned
	// agent process before running the hook. Mirrors the
	// AppendInvestigateEnv contract.
	investigateEnv := []string{
		investigate.EnvSession + "=1",
		investigate.EnvAgent + "=claude-code",
		investigate.EnvRunID + "=" + runID,
		investigate.EnvRound + "=" + strconv.Itoa(round),
		investigate.EnvTurn + "=" + strconv.Itoa(turn),
		investigate.EnvTopic + "=" + topic,
		investigate.EnvPrompt + "=" + userText,
		investigate.EnvFindingsDoc + "=" + findings,
		investigate.EnvStateDoc + "=" + stateP,
		investigate.EnvStartingSHA + "=" + env.GetHeadHash(),
	}

	sess := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithInvestigateEnvVars(sess.ID, userText, investigateEnv); err != nil {
		t.Fatalf("SimulateUserPromptSubmitWithInvestigateEnvVars failed: %v", err)
	}

	state, err := env.GetSessionState(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected investigate session state to be created")
	}
	if state.Kind != session.KindAgentInvestigate {
		t.Fatalf("state.Kind = %q, want %q", state.Kind, session.KindAgentInvestigate)
	}
	if state.InvestigateRunID != runID {
		t.Fatalf("state.InvestigateRunID = %q, want %q", state.InvestigateRunID, runID)
	}
	if state.InvestigateRound != round {
		t.Fatalf("state.InvestigateRound = %d, want %d", state.InvestigateRound, round)
	}
	if state.InvestigateTurn != turn {
		t.Fatalf("state.InvestigateTurn = %d, want %d", state.InvestigateTurn, turn)
	}
	if state.InvestigateTopic != topic {
		t.Fatalf("state.InvestigateTopic = %q, want %q", state.InvestigateTopic, topic)
	}
	if state.InvestigatePrompt != userText {
		t.Fatalf("state.InvestigatePrompt = %q, want %q", state.InvestigatePrompt, userText)
	}

	// Drive the rest of the session: file edit, transcript, stop, commit.
	env.WriteFile("investigate_target.go", "package main\n\nfunc InvestigateTarget() string { return \"ok\" }\n")
	sess.CreateTranscript(userText, []FileChange{
		{Path: "investigate_target.go", Content: "package main\n\nfunc InvestigateTarget() string { return \"ok\" }\n"},
	})
	if err := env.SimulateStop(sess.ID, sess.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	env.GitCommitWithShadowHooks("add investigate target", "investigate_target.go")

	checkpointID := env.GetCheckpointIDFromCommitMessage(env.GetHeadHash())
	if checkpointID == "" {
		t.Fatal("expected Entire-Checkpoint trailer on HEAD after commit")
	}

	summary := readCheckpointSummary(t, env, checkpointID)
	if !summary.HasInvestigation {
		t.Fatalf("summary.HasInvestigation = false for checkpoint %s", checkpointID)
	}

	metadata := readSessionMetadata(t, env, checkpointID)
	if metadata.SessionID != sess.ID {
		t.Fatalf("metadata.SessionID = %q, want %q", metadata.SessionID, sess.ID)
	}
	if metadata.Kind != string(session.KindAgentInvestigate) {
		t.Fatalf("metadata.Kind = %q, want %q", metadata.Kind, session.KindAgentInvestigate)
	}
	if metadata.InvestigateRunID != runID {
		t.Fatalf("metadata.InvestigateRunID = %q, want %q", metadata.InvestigateRunID, runID)
	}
	if metadata.InvestigateRound != round {
		t.Fatalf("metadata.InvestigateRound = %d, want %d", metadata.InvestigateRound, round)
	}
	if metadata.InvestigateTurn != turn {
		t.Fatalf("metadata.InvestigateTurn = %d, want %d", metadata.InvestigateTurn, turn)
	}
	if metadata.InvestigateTopic != topic {
		t.Fatalf("metadata.InvestigateTopic = %q, want %q", metadata.InvestigateTopic, topic)
	}
	if metadata.InvestigatePrompt != userText {
		t.Fatalf("metadata.InvestigatePrompt = %q, want %q", metadata.InvestigatePrompt, userText)
	}
}

// TestInvestigate_FakeAgentLoop_TagsSessionViaLifecycleHook exercises the
// loop-driven investigate adoption pipeline with a fake agent that calls
// back into the entire hooks binary to drive lifecycle adoption.
//
// Simplification (per Task 11 guidance): we drive
// investigate.RunInvestigateLoop directly with a fake spawner rather than
// running the full `entire investigate` cobra command. The spawner uses
// /bin/sh to:
//   - Append a stance block to ENTIRE_INVESTIGATE_TIMELINE_DOC.
//   - Invoke `entire hooks claude-code user-prompt-submit` with the same
//     ENTIRE_INVESTIGATE_* env it inherited, exercising the lifecycle
//     adoption path end-to-end.
//
// What this covers:
//   - The loop populates ENTIRE_INVESTIGATE_* on the spawned process.
//   - The hook child inherits those vars and tags the session.
//   - LoopResult/Outcome reflects the recorded stance.
//
// What this does NOT cover (vs. the full cobra command):
//   - settings.Load + ConfirmFirstRunSetup + picker UI.
//   - Bootstrap / seed-doc resolution.
//   - writeRunManifest. (Manifest writing is exercised separately in unit
//     tests for the manifest package; we don't re-test it here.)
func TestInvestigate_FakeAgentLoop_TagsSessionViaLifecycleHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake agent uses a POSIX shell script")
	}
	t.Parallel()

	env := NewFeatureBranchEnv(t)
	enableInvestigateAgent(t, env, "claude-code")

	const (
		runID    = "abcdef012345"
		topic    = "fake-loop-topic"
		userText = "Please investigate the fake loop topic."
	)
	startingSHA := env.GetHeadHash()

	// Findings doc (alongside the state.json the loop will write).
	stateRoot := t.TempDir()
	findingsDoc := filepath.Join(stateRoot, runID, "findings.md")
	if err := os.MkdirAll(filepath.Dir(findingsDoc), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(findingsDoc, []byte("# Findings\n"), 0o600); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	stateStore := investigate.NewStateStoreWithDir(stateRoot)

	// The fake claude script does two things:
	//   1. Rewrites state.json with pending_turn set to {"stance":"approve"}
	//      via python3 (always available in our CI environment) so the loop
	//      records "approve".
	//   2. Invokes `entire hooks claude-code user-prompt-submit` to drive
	//      lifecycle adoption with the env vars the spawner inherited.
	//
	// The session_id in stdin is read by the lifecycle handler, which
	// writes a session state file the test then reads back.
	sessionID := "investigate-fake-loop-session"
	fakeAgentScript := fmt.Sprintf(`set -eu
python3 -c '
import json, os, sys
p = os.environ["ENTIRE_INVESTIGATE_STATE_DOC"]
with open(p, "r") as f:
    state = json.load(f)
state["pending_turn"] = {"stance": "approve"}
with open(p, "w") as f:
    json.dump(state, f, indent=2)
'
printf '%%s\n' '{"session_id":"%s","transcript_path":"","prompt":"%s"}' | "$ENTIRE_TEST_BINARY" hooks claude-code user-prompt-submit
`, sessionID, userText)

	spawner := &investigateFakeSpawner{
		name:   "claude-code",
		script: fakeAgentScript,
		extraEnv: []string{
			"ENTIRE_TEST_BINARY=" + getTestBinary(),
			"ENTIRE_TEST_CLAUDE_PROJECT_DIR=" + env.ClaudeProjectDir,
			// Force the hook child to operate inside env.RepoDir so it
			// resolves the same git repo the test set up.
			"PWD=" + env.RepoDir,
		},
		dir: env.RepoDir,
	}

	in := investigate.LoopInput{
		RunID:       runID,
		Topic:       topic,
		Agents:      []string{"claude-code"},
		MaxTurns:    1,
		Quorum:      1,
		FindingsDoc: findingsDoc,
		StartingSHA: startingSHA,
	}
	deps := investigate.LoopDeps{
		SpawnerFor: func(name string) spawn.Spawner {
			if name == "claude-code" {
				return spawner
			}
			return nil
		},
		States: stateStore,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := investigate.RunInvestigateLoop(ctx, in, deps)
	if err != nil {
		t.Fatalf("RunInvestigateLoop returned error: %v", err)
	}
	if res.Outcome != investigate.OutcomeQuorum {
		t.Errorf("LoopResult.Outcome = %s, want quorum (claude approved); err=%v", res.Outcome, res.Err)
	}
	if res.State == nil {
		t.Fatal("LoopResult.State is nil")
	}

	// Verify the session was tagged via env-var adoption.
	state, err := env.GetSessionState(sessionID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected lifecycle hook to create session state")
	}
	if state.Kind != session.KindAgentInvestigate {
		t.Errorf("state.Kind = %q, want %q", state.Kind, session.KindAgentInvestigate)
	}
	if state.InvestigateRunID != runID {
		t.Errorf("state.InvestigateRunID = %q, want %q", state.InvestigateRunID, runID)
	}
	if state.InvestigateTopic != topic {
		t.Errorf("state.InvestigateTopic = %q, want %q", state.InvestigateTopic, topic)
	}

	// Verify the loop's per-run StateStore persisted the run state.
	loaded, err := stateStore.Load(ctx, runID)
	if err != nil {
		t.Fatalf("StateStore.Load: %v", err)
	}
	if loaded == nil {
		t.Fatalf("expected persisted run state for %s", runID)
	}
	if len(loaded.Stances) != 1 {
		t.Errorf("Stances = %d, want 1", len(loaded.Stances))
	}
}

// TestInvestigateAttach_TagsAttachedSessionAsInvestigate exercises the
// `entire investigate attach` cobra subcommand: it tags an existing session
// state as agent_investigate and records the run/round/turn/topic/prompt
// fields. After a subsequent commit, the checkpoint summary's
// HasInvestigation flag is set.
//
// Mirror of TestReviewAttach_TagsAttachedSessionAsReview.
func TestInvestigateAttach_TagsAttachedSessionAsInvestigate(t *testing.T) {
	t.Parallel()

	env := NewFeatureBranchEnv(t)

	// Pre-create a session via the normal hook flow so there is a state
	// file to attach to. (entire investigate attach updates an existing
	// session state; it does not create one.)
	sess := env.NewSession()
	if err := env.SimulateUserPromptSubmitWithPrompt(sess.ID, "explore the auth module"); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}

	const (
		runID = "112233445566"
		topic = "explore-auth-module"
	)
	output := env.RunCLI("investigate", "attach", sess.ID, "--run-id", runID, "--topic", topic)
	if !strings.Contains(output, "Tagged session") {
		t.Fatalf("expected 'Tagged session' in output, got:\n%s", output)
	}

	state, err := env.GetSessionState(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected session state")
	}
	if state.Kind != session.KindAgentInvestigate {
		t.Fatalf("state.Kind = %q, want %q", state.Kind, session.KindAgentInvestigate)
	}
	if state.InvestigateRunID != runID {
		t.Fatalf("state.InvestigateRunID = %q, want %q", state.InvestigateRunID, runID)
	}
	if state.InvestigateTopic != topic {
		t.Fatalf("state.InvestigateTopic = %q, want %q", state.InvestigateTopic, topic)
	}

	// Drive the rest of the session: write a file, build a transcript, stop, commit.
	env.WriteFile("investigate_attach_file.go", "package main\n\nfunc Attach() {}\n")
	sess.CreateTranscript("explore the auth module", []FileChange{
		{Path: "investigate_attach_file.go", Content: "package main\n\nfunc Attach() {}\n"},
	})
	if err := env.SimulateStop(sess.ID, sess.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}

	env.GitCommitWithShadowHooks("attach investigate target", "investigate_attach_file.go")

	checkpointID := env.GetCheckpointIDFromCommitMessage(env.GetHeadHash())
	if checkpointID == "" {
		t.Fatal("expected Entire-Checkpoint trailer on HEAD after commit")
	}

	summary := readCheckpointSummary(t, env, checkpointID)
	if !summary.HasInvestigation {
		t.Fatalf("summary.HasInvestigation = false for checkpoint %s", checkpointID)
	}

	metadata := readSessionMetadata(t, env, checkpointID)
	if metadata.Kind != string(session.KindAgentInvestigate) {
		t.Fatalf("metadata.Kind = %q, want %q", metadata.Kind, session.KindAgentInvestigate)
	}
	if metadata.InvestigateRunID != runID {
		t.Fatalf("metadata.InvestigateRunID = %q, want %q", metadata.InvestigateRunID, runID)
	}
	if metadata.InvestigateTopic != topic {
		t.Fatalf("metadata.InvestigateTopic = %q, want %q", metadata.InvestigateTopic, topic)
	}
}

// TestInvestigate_Continue_ResumesAtRecordedAgentIdx exercises the resume
// path: a pre-seeded RunState with NextAgentIdx=1 must cause the next
// spawned agent to be agents[1], not agents[0].
//
// Simplification (per Task 11 guidance): we drive RunInvestigateLoop
// directly with LoopInput.Resume rather than running `entire investigate
// --continue`. The cobra command's --continue path (runContinue in
// investigate/cmd.go) is a thin wrapper that loads the persisted RunState
// and feeds it into LoopInput.Resume; this test pins that wrapper's
// contract by exercising the loop with a synthetic Resume state.
func TestInvestigate_Continue_ResumesAtRecordedAgentIdx(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake agent uses a POSIX shell script")
	}
	t.Parallel()

	stateRoot := t.TempDir()
	stateStore := investigate.NewStateStoreWithDir(stateRoot)

	// Pre-seed: claude-code already went, codex is next (NextAgentIdx=1).
	const runID = "fedcba987654"
	findings := filepath.Join(stateRoot, runID, "findings.md")
	if err := os.MkdirAll(filepath.Dir(findings), 0o755); err != nil {
		t.Fatalf("MkdirAll findings: %v", err)
	}
	if err := os.WriteFile(findings, []byte("# Findings\n"), 0o600); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	resume := &investigate.RunState{
		RunID:           runID,
		Topic:           "resume-topic",
		Agents:          []string{"claude-code", "codex"},
		MaxTurns:        1,
		Quorum:          2,
		CompletedRounds: 0,
		Turn:            1,
		NextAgentIdx:    1,
		Stances: []investigate.TurnStance{
			{Round: 1, Turn: 1, Agent: "claude-code", Stance: "approve"},
		},
		FindingsDoc: findings,
		StartingSHA: "deadbeef",
		StartedAt:   time.Now().Add(-time.Hour).UTC(),
		UpdatedAt:   time.Now().Add(-time.Hour).UTC(),
	}
	if err := stateStore.Save(context.Background(), resume); err != nil {
		t.Fatalf("Save resume state: %v", err)
	}

	loaded, err := stateStore.Load(context.Background(), runID)
	if err != nil || loaded == nil {
		t.Fatalf("Load: state=%v err=%v", loaded, err)
	}

	var observedAgents []string
	spawnerFor := func(name string) spawn.Spawner {
		return &investigateFakeSpawner{
			name: name,
			script: `set -eu
python3 -c '
import json, os
p = os.environ["ENTIRE_INVESTIGATE_STATE_DOC"]
with open(p, "r") as f:
    state = json.load(f)
state["pending_turn"] = {"stance": "approve"}
with open(p, "w") as f:
    json.dump(state, f, indent=2)
'
`,
			onSpawn: func() {
				observedAgents = append(observedAgents, name)
			},
		}
	}

	in := investigate.LoopInput{
		RunID:       runID,
		Topic:       resume.Topic,
		Agents:      resume.Agents,
		MaxTurns:    1,
		Quorum:      2,
		FindingsDoc: findings,
		StartingSHA: resume.StartingSHA,
		Resume:      loaded,
	}
	deps := investigate.LoopDeps{
		SpawnerFor: spawnerFor,
		States:     stateStore,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := investigate.RunInvestigateLoop(ctx, in, deps)
	if err != nil {
		t.Fatalf("RunInvestigateLoop: %v", err)
	}
	if res.Outcome != investigate.OutcomeQuorum {
		t.Errorf("Outcome = %s, want quorum after resume completes round; err=%v", res.Outcome, res.Err)
	}
	if len(observedAgents) == 0 {
		t.Fatal("no agents were spawned on resume")
	}
	if observedAgents[0] != "codex" {
		t.Errorf("first spawned agent on resume = %q, want codex", observedAgents[0])
	}
}

// TestInvestigate_IssueLink_ResolvesViaFakeGh runs `entire investigate` with
// a fake `gh` binary on PATH that returns canned issue JSON. Asserts that
// the bootstrapped findings doc contains the issue title (used as topic)
// and that the seed-doc body carries the fixture body and at least one
// comment.
//
// We pass --max-turns 1 with a fake claude that just exits 0 (no stance),
// causing the loop to terminate stalled after one turn — far enough to
// confirm bootstrap ran. We then inspect the on-disk findings doc (under
// .entire/investigations/<slug>.md) for the resolved title + body.
func TestInvestigate_IssueLink_ResolvesViaFakeGh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh + fake claude rely on POSIX shell scripts")
	}
	t.Parallel()

	env := NewFeatureBranchEnv(t)
	enableInvestigateAgent(t, env, "claude-code")
	env.WriteSettings(map[string]any{
		"enabled": true,
		"investigate": map[string]any{
			"agents":    []string{"claude-code"},
			"max_turns": 1,
			"quorum":    1,
		},
	})

	// Stage fake binaries on PATH. Layout:
	//   <fakeBinDir>/
	//       gh        — returns canned issue JSON for `gh issue view`
	//       claude    — exits 0 (loop will record an unknown stance)
	fakeBinDir := t.TempDir()

	const issueTitle = "Why is checkout flaky?"
	const issueBody = "Checkout occasionally fails on Tuesdays."
	const commentBody = "I see this on Linux only."
	ghJSON := fmt.Sprintf(`{
  "title": %q,
  "body": %q,
  "author": {"login": "octocat"},
  "createdAt": "2026-01-01T00:00:00Z",
  "labels": [{"name": "flake"}],
  "comments": [
    {"author": {"login": "hubot"}, "createdAt": "2026-01-02T00:00:00Z", "body": %q}
  ]
}`, issueTitle, issueBody, commentBody)
	// Write JSON via a heredoc-style cat to avoid shell escaping headaches.
	ghJSONFile := filepath.Join(fakeBinDir, "issue.json")
	if err := os.WriteFile(ghJSONFile, []byte(ghJSON), 0o644); err != nil {
		t.Fatalf("write issue fixture: %v", err)
	}
	ghScript := "#!/bin/sh\nexec cat " + ghJSONFile + "\n"
	ghPath := filepath.Join(fakeBinDir, "gh")
	if err := os.WriteFile(ghPath, []byte(ghScript), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	// Fake claude: just exit 0 so the loop completes without recording a
	// stance. We're only asserting bootstrap + issue resolution here.
	claudeScript := "#!/bin/sh\nexit 0\n"
	claudePath := filepath.Join(fakeBinDir, "claude")
	if err := os.WriteFile(claudePath, []byte(claudeScript), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := execx.NonInteractive(ctx, getTestBinary(),
		"investigate",
		"--issue-link", "https://github.com/foo/bar/issues/1",
		"--max-turns", "1",
		"--agents", "claude-code")
	cmd.Dir = env.RepoDir
	cmd.Env = envWithOverrides(env.cliEnv(),
		"PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ENTIRE_TEST_BINARY="+getTestBinary(),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("entire investigate failed: %v\nOutput:\n%s", err, output)
	}

	// Find the bootstrapped findings doc. The per-run dir lives under the
	// git common dir; we glob the entire-investigations directory rather
	// than re-deriving the run ID, which keeps the test resilient to
	// implementation tweaks.
	investigationsDir := filepath.Join(env.RepoDir, ".git", "entire-investigations")
	entries, err := os.ReadDir(investigationsDir)
	if err != nil {
		t.Fatalf("read .git/entire-investigations: %v\nOutput:\n%s", err, output)
	}
	var foundFindings string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "manifests" {
			continue
		}
		candidate := filepath.Join(investigationsDir, name, "findings.md")
		if _, statErr := os.Stat(candidate); statErr == nil {
			foundFindings = candidate
			break
		}
	}
	if foundFindings == "" {
		t.Fatalf("no findings doc found under %s\nOutput:\n%s", investigationsDir, output)
	}

	body, err := os.ReadFile(foundFindings)
	if err != nil {
		t.Fatalf("read findings doc: %v", err)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, issueTitle) {
		t.Errorf("findings doc missing issue title %q\n%s", issueTitle, bodyStr)
	}
	if !strings.Contains(bodyStr, issueBody) {
		t.Errorf("findings doc missing issue body %q\n%s", issueBody, bodyStr)
	}
	if !strings.Contains(bodyStr, commentBody) {
		t.Errorf("findings doc missing comment %q\n%s", commentBody, bodyStr)
	}
}

// --- helpers --------------------------------------------------------------

// enableInvestigateAgent installs the named agent's hooks via `entire enable`.
// Mirrors enableReviewAgent.
func enableInvestigateAgent(t *testing.T, env *TestEnv, name string) {
	t.Helper()
	env.RunCLI("enable", "--agent", name, "--telemetry=false")
}

// SimulateUserPromptSubmitWithInvestigateEnvVars fires UserPromptSubmit with
// the given prompt and a set of ENTIRE_INVESTIGATE_* env vars on the hook
// child process. Mirrors SimulateUserPromptSubmitWithReviewEnvVars.
func (env *TestEnv) SimulateUserPromptSubmitWithInvestigateEnvVars(sessionID, prompt string, extraEnv []string) error {
	env.T.Helper()
	runner := NewHookRunner(env.RepoDir, env.ClaudeProjectDir, env.T)
	// Reuse the runner's review-env helper: it just appends extraEnv
	// verbatim on top of the hook subprocess env, so it works for any
	// ENTIRE_*_* vars regardless of name.
	return runner.SimulateUserPromptSubmitWithReviewEnvVars(sessionID, prompt, extraEnv)
}

// investigateFakeSpawner is a spawn.Spawner whose BuildCmd returns a
// /bin/sh process running a canned script with ENTIRE_INVESTIGATE_* +
// extra env. The script may also write a stance to the timeline file
// (resolved via $ENTIRE_INVESTIGATE_TIMELINE_DOC) and call back into the
// real entire test binary to drive lifecycle hooks.
type investigateFakeSpawner struct {
	name     string
	script   string
	extraEnv []string
	dir      string
	onSpawn  func()
}

func (s *investigateFakeSpawner) Name() string { return s.name }

func (s *investigateFakeSpawner) BuildCmd(ctx context.Context, env []string, _ string) *exec.Cmd {
	if s.onSpawn != nil {
		s.onSpawn()
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", s.script)
	cmd.Env = append(append([]string(nil), env...), s.extraEnv...)
	if s.dir != "" {
		cmd.Dir = s.dir
	}
	return cmd
}
