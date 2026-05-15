// Package investigate — see env.go for package-level rationale.
//
// findings.go implements `entire investigate --findings`: list locally
// persisted LocalManifests and (in TTY mode) drill into one for details.
package investigate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/entireio/cli/cmd/entire/cli/paths"
)

// runInvestigateFindings handles `entire investigate --findings`. In a
// TTY it shows a huh selector and prints the chosen manifest's details;
// in non-TTY mode it prints a plain list with `entire investigate fix
// <run-id>` hints. Mirrors review.runReviewFindings in shape.
func runInvestigateFindings(ctx context.Context, cmd *cobra.Command, silentErr func(error) error) error {
	if _, err := paths.WorktreeRoot(ctx); err != nil {
		cmd.SilenceUsage = true
		fmt.Fprintln(cmd.ErrOrStderr(), "Not a git repository. Run `entire enable` first.")
		return wrapSilent(silentErr, errors.New("not a git repository"))
	}
	store, err := NewLocalManifestStore(ctx)
	if err != nil {
		return fmt.Errorf("open manifest store: %w", err)
	}
	manifests, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("list manifests: %w", err)
	}
	if len(manifests) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No local investigations found.")
		return nil
	}
	// Always print the full list. The previous TTY behaviour opened a
	// single-select picker that surfaced just one manifest's detail,
	// which hid the rest of the run history from users who reached for
	// --findings precisely BECAUSE they wanted to see all runs. The
	// `fix:` hint per row gives them the next step.
	printInvestigateFindingsList(cmd.OutOrStdout(), manifests)
	return nil
}

// PrintInvestigateFindingsListForTest exposes printInvestigateFindingsList
// to tests in package investigate_test. Production callers should use
// runInvestigateFindings via the cobra command instead.
func PrintInvestigateFindingsListForTest(w io.Writer, manifests []LocalManifest) {
	printInvestigateFindingsList(w, manifests)
}

// printInvestigateFindingsList renders the non-TTY list view. Each
// manifest gets a header row plus a "fix" command hint.
func printInvestigateFindingsList(w io.Writer, manifests []LocalManifest) {
	fmt.Fprintln(w, "Investigations")
	fmt.Fprintln(w)
	for _, m := range manifests {
		fmt.Fprintln(w, investigateManifestListLabel(m))
		fmt.Fprintf(w, "  fix:     entire investigate fix %s\n", m.RunID)
		if m.FindingsDoc != "" {
			fmt.Fprintf(w, "  findings: %s\n", m.FindingsDoc)
		}
	}
}

// investigateManifestListLabel formats one manifest for picker / list
// display. Format: "<run-id> · <topic> · <agents> · <relative-time>".
func investigateManifestListLabel(m LocalManifest) string {
	when := relativeTimeLabel(m.StartedAt)
	parts := []string{m.RunID}
	if topic := strings.TrimSpace(m.Topic); topic != "" {
		parts = append(parts, topic)
	}
	if len(m.Agents) > 0 {
		parts = append(parts, strings.Join(m.Agents, ", "))
	}
	if when != "" {
		parts = append(parts, when)
	}
	return strings.Join(parts, " · ")
}

// relativeTimeLabel formats t as a coarse "Nm ago" / "Nh ago" / "Nd ago"
// string suitable for picker labels. Returns the empty string for the
// zero value.
func relativeTimeLabel(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
