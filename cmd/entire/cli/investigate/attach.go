package investigate

// attach.go provides the `entire investigate attach <session-id>` cobra
// subcommand. It mirrors the shape of `entire review attach`: a thin
// cobra wrapper that delegates to a state-tagging helper injected via
// AttachDeps.Attach. Production wires Attach to the cli package's
// AttachSession function (see review_bridge.go / investigate bridge wiring
// in the cli package).
//
// This is the manual / power-user path. Day-to-day investigate tagging
// happens automatically via env-var adoption in lifecycle.go, which is
// why the subcommand keeps validation minimal.

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// AttachDeps collects what NewAttachCommand needs from the cli package.
//
// Production wires Attach to the cli package's AttachSession via the
// bridge file (so investigate does not import cli, avoiding a cycle).
// Tests inject a stub Attach to capture arguments without touching state.
type AttachDeps struct {
	// Attach tags an existing session as agent_investigate. Production
	// wires this to AttachSession(ctx, AttachOptions{Kind:
	// AttachKindInvestigate, ...}). Required.
	Attach func(ctx context.Context, sessionID string, runID string, round, turn int, topic, prompt string) error

	// NewSilentError wraps an error so the cobra root does not double-print
	// it. Optional: when nil, errors are returned raw and cobra's default
	// printing applies.
	NewSilentError func(err error) error
}

// NewAttachCommand returns the `entire investigate attach <session-id>`
// cobra subcommand. The returned command takes <session-id> as its
// single positional argument and supports flags --run-id, --round,
// --turn, --topic, --prompt. At least one of --run-id or --topic must
// be set (otherwise the attach has no investigate metadata to record).
func NewAttachCommand(deps AttachDeps) *cobra.Command {
	var (
		runIDFlag  string
		roundFlag  int
		turnFlag   int
		topicFlag  string
		promptFlag string
	)

	cmd := &cobra.Command{
		Use:   "attach <session-id>",
		Short: "Tag an existing agent session as an investigation",
		Long: `Tag an existing agent session as agent_investigate and record
investigation metadata (run id, round, turn, topic, prompt) on its
session state.

This is the manual / power-user path. Day-to-day investigate tagging
happens automatically when the agent is launched by 'entire investigate'.

At least one of --run-id or --topic must be supplied. When --run-id is
set, it must be a 12-hex-char investigation run ID.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if deps.Attach == nil {
				return errors.New("investigate attach: Attach dep is not wired")
			}

			if err := validateAttachFlags(runIDFlag, topicFlag); err != nil {
				cmd.SilenceUsage = true
				if deps.NewSilentError != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
					return deps.NewSilentError(err)
				}
				return err
			}

			err := deps.Attach(cmd.Context(), args[0], runIDFlag, roundFlag, turnFlag, topicFlag, promptFlag)
			if err != nil {
				cmd.SilenceUsage = true
				if deps.NewSilentError != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
					return deps.NewSilentError(err)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Tagged session %s as agent_investigate\n", args[0])
			return nil
		},
	}

	cmd.Flags().StringVar(&runIDFlag, "run-id", "", "Investigation run ID (12 hex chars)")
	cmd.Flags().IntVar(&roundFlag, "round", 0, "Round number within the investigation run")
	cmd.Flags().IntVar(&turnFlag, "turn", 0, "Overall turn index across rounds")
	cmd.Flags().StringVar(&topicFlag, "topic", "", "Topic the investigation was asked to investigate")
	cmd.Flags().StringVar(&promptFlag, "prompt", "", "Prompt sent to the agent for this turn")
	return cmd
}

// validateAttachFlags enforces the minimal flag rules: at least one of
// --run-id or --topic must be set, and --run-id (when present) must be
// 12 hex chars. Kept separate from the cobra command so tests can drive
// it without going through Execute.
func validateAttachFlags(runID, topic string) error {
	if runID == "" && topic == "" {
		return errors.New("investigate attach: at least one of --run-id or --topic must be set")
	}
	if runID != "" && !runIDPattern.MatchString(runID) {
		return fmt.Errorf("investigate attach: --run-id %q is not a 12-hex-char investigation run id", runID)
	}
	return nil
}
