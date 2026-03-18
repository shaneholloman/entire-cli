package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	"github.com/entireio/cli/cmd/entire/cli/agent/geminicli"
	"github.com/entireio/cli/cmd/entire/cli/agent/types"
	"github.com/entireio/cli/cmd/entire/cli/logging"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/session"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
	"github.com/entireio/cli/cmd/entire/cli/trailers"
	"github.com/entireio/cli/cmd/entire/cli/validation"
	"github.com/entireio/cli/cmd/entire/cli/versioninfo"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
)

func newAttachCmd() *cobra.Command {
	var (
		force     bool
		agentFlag string
	)
	cmd := &cobra.Command{
		Use:   "attach <session-id>",
		Short: "Attach an existing agent session",
		Long: `Attach an existing agent session that wasn't captured by hooks.

This creates a checkpoint from the session's transcript and registers the
session for future tracking. Use this when hooks failed to fire or weren't
installed when the session started.

Supported agents: claude-code, gemini, opencode, cursor, copilot-cli, factoryai-droid`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkDisabledGuard(cmd.Context(), cmd.OutOrStdout()) {
				return nil
			}
			agentName := types.AgentName(agentFlag)
			return runAttach(cmd.Context(), cmd.OutOrStdout(), args[0], agentName, force)
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation and amend the last commit with the checkpoint trailer")
	cmd.Flags().StringVarP(&agentFlag, "agent", "a", string(agent.DefaultAgentName), "Agent that created the session (claude-code, gemini, opencode, cursor, copilot-cli, factoryai-droid)")
	return cmd
}

func runAttach(ctx context.Context, w io.Writer, sessionID string, agentName types.AgentName, force bool) error {
	logCtx := logging.WithComponent(ctx, "attach")

	repoRoot, err := validateAttachPreconditions(ctx, sessionID)
	if err != nil {
		return err
	}

	ag, transcriptPath, err := resolveAgentAndTranscript(logCtx, w, sessionID, agentName)
	if err != nil {
		return err
	}
	agentType := ag.Type()

	transcriptData, err := ag.ReadTranscript(transcriptPath)
	if err != nil {
		return fmt.Errorf("failed to read transcript: %w", err)
	}

	relModifiedFiles, relNewFiles, relDeletedFiles, fileDetectionFailed := collectFileChanges(ctx, logCtx, ag, transcriptPath, repoRoot)

	if err := strategy.EnsureSetup(ctx); err != nil {
		return fmt.Errorf("failed to set up strategy: %w", err)
	}

	logFile, sessionDir, sessionDirAbs, err := storeTranscript(ctx, sessionID, agentType, transcriptData)
	if err != nil {
		return err
	}

	author, err := GetGitAuthor(ctx)
	if err != nil {
		return fmt.Errorf("failed to get git author: %w", err)
	}

	meta := extractTranscriptMetadata(transcriptData)
	firstPrompt := meta.FirstPrompt
	commitMessage := generateCommitMessage(firstPrompt, agentType)

	if firstPrompt != "" {
		promptFile := filepath.Join(sessionDirAbs, paths.PromptFileName)
		if err := os.WriteFile(promptFile, []byte(firstPrompt), 0o600); err != nil {
			logging.Warn(logCtx, "failed to write prompt file", "error", err)
		}
	}

	strat := GetStrategy(ctx)
	if err := strat.InitializeSession(ctx, sessionID, agentType, logFile, firstPrompt, ""); err != nil {
		return fmt.Errorf("failed to initialize session: %w", err)
	}

	if err := enrichSessionState(logCtx, sessionID, ag, transcriptData, logFile, meta); err != nil {
		return err
	}

	totalChanges := len(relModifiedFiles) + len(relNewFiles) + len(relDeletedFiles)
	stepCtx := strategy.StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  relModifiedFiles,
		NewFiles:       relNewFiles,
		DeletedFiles:   relDeletedFiles,
		MetadataDir:    sessionDir,
		MetadataDirAbs: sessionDirAbs,
		CommitMessage:  commitMessage,
		TranscriptPath: logFile,
		AuthorName:     author.Name,
		AuthorEmail:    author.Email,
		AgentType:      agentType,
	}

	if err := strat.SaveStep(ctx, stepCtx); err != nil {
		return fmt.Errorf("failed to save checkpoint: %w", err)
	}

	checkpointIDStr, condenseErr := condenseAndFinalizeSession(logCtx, strat, sessionID)

	printAttachConfirmation(w, sessionID, totalChanges, fileDetectionFailed, condenseErr)

	if checkpointIDStr != "" {
		if err := promptAmendCommit(ctx, w, checkpointIDStr, force); err != nil {
			logging.Warn(logCtx, "failed to amend commit", "error", err)
			fmt.Fprintf(w, "\nCopy to your commit message to attach:\n\n  Entire-Checkpoint: %s\n", checkpointIDStr)
		}
	}

	return nil
}

// validateAttachPreconditions checks session ID format, git repo state, and duplicate sessions.
func validateAttachPreconditions(ctx context.Context, sessionID string) (string, error) {
	if err := validation.ValidateSessionID(sessionID); err != nil {
		return "", fmt.Errorf("invalid session ID: %w", err)
	}

	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	repo, repoErr := strategy.OpenRepository(ctx)
	if repoErr != nil {
		return "", fmt.Errorf("failed to open repository: %w", repoErr)
	}
	if strategy.IsEmptyRepository(repo) {
		return "", errors.New("repository has no commits yet — make an initial commit before running attach")
	}

	store, err := session.NewStateStore(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to open session store: %w", err)
	}
	existing, err := store.Load(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to check existing session: %w", err)
	}
	if existing != nil {
		return "", fmt.Errorf("session %s is already tracked by Entire", sessionID)
	}

	return repoRoot, nil
}

// resolveAgentAndTranscript resolves the agent and transcript path, with auto-detection fallback.
func resolveAgentAndTranscript(ctx context.Context, w io.Writer, sessionID string, agentName types.AgentName) (agent.Agent, string, error) {
	ag, err := agent.Get(agentName)
	if err != nil {
		return nil, "", fmt.Errorf("agent %q not available: %w", agentName, err)
	}

	transcriptPath, err := resolveAndValidateTranscript(ctx, sessionID, ag)
	if err != nil {
		detectedAg, detectedPath, detectErr := detectAgentByTranscript(ctx, sessionID, agentName)
		if detectErr != nil {
			return nil, "", err
		}
		ag = detectedAg
		transcriptPath = detectedPath
		logging.Info(ctx, "auto-detected agent from transcript", "agent", ag.Name())
		fmt.Fprintf(w, "Auto-detected agent: %s\n", ag.Name())
	}

	return ag, transcriptPath, nil
}

// collectFileChanges gathers modified, new, and deleted files from both transcript analysis and git status.
// Returns detectionFailed=true if file change detection errored (distinct from finding no changes).
func collectFileChanges(ctx, logCtx context.Context, ag agent.Agent, transcriptPath, repoRoot string) (modified, added, deleted []string, detectionFailed bool) {
	var transcriptFiles []string
	if analyzer, ok := agent.AsTranscriptAnalyzer(ag); ok {
		if files, _, fileErr := analyzer.ExtractModifiedFilesFromOffset(transcriptPath, 0); fileErr != nil {
			logging.Warn(logCtx, "failed to extract modified files from transcript", "error", fileErr)
			detectionFailed = true
		} else {
			transcriptFiles = files
		}
	}

	changes, err := DetectFileChanges(ctx, nil)
	if err != nil {
		logging.Warn(logCtx, "failed to detect file changes, checkpoint may be incomplete", "error", err)
		detectionFailed = true
	}

	modified = FilterAndNormalizePaths(transcriptFiles, repoRoot)
	if changes != nil {
		added = FilterAndNormalizePaths(changes.New, repoRoot)
		deleted = FilterAndNormalizePaths(changes.Deleted, repoRoot)
		modified = mergeUnique(modified, FilterAndNormalizePaths(changes.Modified, repoRoot))
	}

	modified = filterToUncommittedFiles(ctx, modified, repoRoot)
	return modified, added, deleted, detectionFailed
}

// storeTranscript creates the session metadata directory and writes the (optionally normalized) transcript.
func storeTranscript(ctx context.Context, sessionID string, agentType types.AgentType, transcriptData []byte) (logFile, sessionDir, sessionDirAbs string, err error) {
	logCtx := logging.WithComponent(ctx, "attach")
	sessionDir = paths.SessionMetadataDirFromSessionID(sessionID)
	sessionDirAbs, err = paths.AbsPath(ctx, sessionDir)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to resolve session directory path: %w", err)
	}
	if err := os.MkdirAll(sessionDirAbs, 0o750); err != nil {
		return "", "", "", fmt.Errorf("failed to create session directory: %w", err)
	}

	storedTranscript := transcriptData
	if agentType == agent.AgentTypeGemini {
		if normalized, normErr := geminicli.NormalizeTranscript(transcriptData); normErr == nil {
			storedTranscript = normalized
		} else {
			logging.Warn(logCtx, "failed to normalize Gemini transcript, storing raw", "error", normErr)
		}
	}

	logFile = filepath.Join(sessionDirAbs, paths.TranscriptFileName)
	if err := os.WriteFile(logFile, storedTranscript, 0o600); err != nil {
		return "", "", "", fmt.Errorf("failed to write transcript: %w", err)
	}
	return logFile, sessionDir, sessionDirAbs, nil
}

// printAttachConfirmation prints the post-attach status message.
func printAttachConfirmation(w io.Writer, sessionID string, totalChanges int, fileDetectionFailed bool, condenseErr error) {
	fmt.Fprintf(w, "Attached session %s\n", sessionID)
	switch {
	case totalChanges > 0:
		fmt.Fprintf(w, "  Checkpoint saved with %d file(s)\n", totalChanges)
	case fileDetectionFailed:
		fmt.Fprintln(w, "  Checkpoint saved (transcript only, file change detection failed)")
	default:
		fmt.Fprintln(w, "  Checkpoint saved (transcript only, no uncommitted file changes detected)")
	}
	if condenseErr != nil {
		fmt.Fprintln(w, "  Warning: checkpoint saved on shadow branch only (condensation failed)")
	}
	fmt.Fprintln(w, "  Session is now tracked — future prompts will be captured automatically")
}

// enrichSessionState loads the session state after initialization and populates it with
// transcript-derived metadata (token usage, turn count, model name, duration).
// The meta parameter provides pre-extracted prompt/turn/model data to avoid re-parsing.
func enrichSessionState(ctx context.Context, sessionID string, ag agent.Agent, transcriptData []byte, transcriptPath string, meta transcriptMetadata) error {
	state, err := strategy.LoadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("session state not found after initialization (session_id=%s)", sessionID)
	}
	state.CLIVersion = versioninfo.Version
	state.TranscriptPath = transcriptPath

	if usage := agent.CalculateTokenUsage(ctx, ag, transcriptData, 0, ""); usage != nil {
		state.TokenUsage = usage
	}
	if meta.TurnCount > 0 {
		state.SessionTurnCount = meta.TurnCount
	}
	if meta.Model != "" {
		state.ModelName = meta.Model
	}
	if dur := estimateSessionDuration(transcriptData); dur > 0 {
		state.SessionDurationMs = dur
	}

	if err := strategy.SaveSessionState(ctx, state); err != nil {
		return fmt.Errorf("failed to save session state: %w", err)
	}
	return nil
}

// condenseAndFinalizeSession condenses the session to permanent storage and transitions it to IDLE.
// Returns the checkpoint ID string and any condensation error.
// Note: accepts *strategy.ManualCommitStrategy directly because CondenseSessionByID is intentionally
// not on the Strategy interface — it's a strategy-specific repair/attach operation.
func condenseAndFinalizeSession(ctx context.Context, strat *strategy.ManualCommitStrategy, sessionID string) (string, error) {
	var checkpointIDStr string
	var condenseErr error
	if err := strat.CondenseSessionByID(ctx, sessionID); err != nil {
		logging.Warn(ctx, "failed to condense session", "error", err, "session_id", sessionID)
		condenseErr = err
	}

	// Single load serves both checkpoint ID extraction and finalization
	state, loadErr := strategy.LoadSessionState(ctx, sessionID)
	if loadErr != nil {
		logging.Warn(ctx, "failed to load session state after condensation", "error", loadErr, "session_id", sessionID)
		return checkpointIDStr, condenseErr
	}
	if state == nil {
		return checkpointIDStr, condenseErr
	}

	if condenseErr == nil {
		checkpointIDStr = state.LastCheckpointID.String()
	}

	now := time.Now()
	state.LastInteractionTime = &now
	if transErr := strategy.TransitionAndLog(ctx, state, session.EventTurnEnd, session.TransitionContext{}, session.NoOpActionHandler{}); transErr != nil {
		logging.Warn(ctx, "failed to transition session to idle", "error", transErr, "session_id", sessionID)
	}
	if saveErr := strategy.SaveSessionState(ctx, state); saveErr != nil {
		logging.Warn(ctx, "failed to save session state after transition", "error", saveErr, "session_id", sessionID)
	}

	return checkpointIDStr, condenseErr
}

// resolveAndValidateTranscript finds the transcript file for a session, searching alternative
// project directories if needed.
func resolveAndValidateTranscript(ctx context.Context, sessionID string, ag agent.Agent) (string, error) {
	transcriptPath, err := resolveTranscriptPath(ctx, sessionID, ag)
	if err != nil {
		return "", fmt.Errorf("failed to resolve transcript path: %w", err)
	}
	// If agent implements TranscriptPreparer, materialize the transcript before checking disk.
	if preparer, ok := agent.AsTranscriptPreparer(ag); ok {
		if prepErr := preparer.PrepareTranscript(ctx, transcriptPath); prepErr != nil {
			logging.Debug(ctx, "PrepareTranscript failed (best-effort)", "error", prepErr)
		}
	}
	// Agents use cwd-derived project directories, so the transcript may be stored under
	// a different project directory if the session was started from a different working directory.
	if _, statErr := os.Stat(transcriptPath); statErr == nil {
		return transcriptPath, nil
	}
	found, searchErr := searchTranscriptInProjectDirs(sessionID, ag)
	if searchErr == nil {
		logging.Info(ctx, "found transcript in alternative project directory", "path", found)
		return found, nil
	}
	logging.Debug(ctx, "fallback transcript search failed", "error", searchErr)
	return "", fmt.Errorf("transcript not found for agent %q with session %s; is the session ID correct?", ag.Name(), sessionID)
}

// detectAgentByTranscript tries all registered agents (except skip) to find one whose
// transcript resolution succeeds for the given session ID.
func detectAgentByTranscript(ctx context.Context, sessionID string, skip types.AgentName) (agent.Agent, string, error) {
	for _, name := range agent.List() {
		if name == skip {
			continue
		}
		ag, err := agent.Get(name)
		if err != nil {
			continue
		}
		path, resolveErr := resolveAndValidateTranscript(ctx, sessionID, ag)
		if resolveErr != nil {
			logging.Debug(ctx, "auto-detect: agent did not match", "agent", string(name), "error", resolveErr)
			continue
		}
		return ag, path, nil
	}
	return nil, "", errors.New("transcript not found for any registered agent")
}

// promptAmendCommit shows the last commit and asks whether to amend it with the checkpoint trailer.
// When force is true, it amends without prompting.
func promptAmendCommit(ctx context.Context, w io.Writer, checkpointIDStr string, force bool) error {
	// Get HEAD commit info
	repo, err := openRepository(ctx)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	headRef, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}
	headCommit, err := repo.CommitObject(headRef.Hash())
	if err != nil {
		return fmt.Errorf("failed to get HEAD commit: %w", err)
	}

	shortHash := headRef.Hash().String()[:7]
	subject := strings.SplitN(headCommit.Message, "\n", 2)[0]

	fmt.Fprintf(w, "\nLast commit: %s %s\n", shortHash, subject)

	amend := true
	if !force {
		form := NewAccessibleForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Amend the last commit in this branch?").
					Affirmative("Y").
					Negative("n").
					Value(&amend),
			),
		)
		if err := form.Run(); err != nil {
			return fmt.Errorf("prompt failed: %w", err)
		}
	}

	if !amend {
		fmt.Fprintf(w, "\nCopy to your commit message to attach:\n\n  Entire-Checkpoint: %s\n", checkpointIDStr)
		return nil
	}

	// Skip amending if this exact checkpoint ID is already in the commit
	for _, existing := range trailers.ParseAllCheckpoints(headCommit.Message) {
		if existing.String() == checkpointIDStr {
			fmt.Fprintf(w, "Commit already has Entire-Checkpoint: %s (skipping amend)\n", checkpointIDStr)
			return nil
		}
	}

	// Amend the commit with the checkpoint trailer.
	newMessage := trailers.AppendCheckpointTrailer(headCommit.Message, checkpointIDStr)

	// --only ensures this amend updates the commit message only and does not
	// accidentally include unrelated staged changes.
	cmd := exec.CommandContext(ctx, "git", "commit", "--amend", "--only", "-m", newMessage)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to amend commit: %w\n%s", err, output)
	}

	fmt.Fprintf(w, "Amended commit %s with Entire-Checkpoint: %s\n", shortHash, checkpointIDStr)
	return nil
}
