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
	"sort"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/entireio/cli/cmd/entire/cli/interactive"
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
	if interactive.IsTerminalWriter(cmd.OutOrStdout()) && interactive.CanPromptInteractively() {
		picked, pickErr := promptForInvestigateManifest(ctx, manifests)
		if pickErr != nil {
			return pickErr
		}
		printInvestigateManifestDetail(cmd.OutOrStdout(), picked)
		return nil
	}
	printInvestigateFindingsList(cmd.OutOrStdout(), manifests)
	return nil
}

// promptForInvestigateManifest renders a single-select picker over
// manifests sorted newest-first. Returns the selected manifest.
func promptForInvestigateManifest(ctx context.Context, manifests []LocalManifest) (LocalManifest, error) {
	sorted := append([]LocalManifest(nil), manifests...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].StartedAt.After(sorted[j].StartedAt)
	})
	options := make([]huh.Option[int], len(sorted))
	for i, m := range sorted {
		options[i] = huh.NewOption(investigateManifestListLabel(m), i)
	}
	picked := 0
	form := newAccessibleForm(huh.NewGroup(
		huh.NewSelect[int]().
			Title("Select an investigation").
			Options(options...).
			Height(min(len(options)+1, 10)).
			Value(&picked),
	))
	if err := form.RunWithContext(ctx); err != nil {
		return LocalManifest{}, fmt.Errorf("investigation picker: %w", err)
	}
	return sorted[picked], nil
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
		if m.TimelineDoc != "" {
			fmt.Fprintf(w, "  timeline: %s\n", m.TimelineDoc)
		}
	}
}

// printInvestigateManifestDetail prints the per-manifest detail view
// shown after a TTY pick. Includes agent stances and doc paths.
func printInvestigateManifestDetail(w io.Writer, m LocalManifest) {
	fmt.Fprintf(w, "Investigation findings from %s\n\n", investigateManifestListLabel(m))
	if m.FindingsDoc != "" {
		fmt.Fprintf(w, "Findings: %s\n", m.FindingsDoc)
	}
	if m.TimelineDoc != "" {
		fmt.Fprintf(w, "Timeline: %s\n", m.TimelineDoc)
	}
	if len(m.Agents) > 0 {
		fmt.Fprintf(w, "Agents:   %s\n", strings.Join(m.Agents, ", "))
	}
	if m.Outcome != "" {
		fmt.Fprintf(w, "Outcome:  %s\n", m.Outcome)
	}
	if len(m.StancesByAgent) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Last stance per agent:")
		// Sort for deterministic output.
		keys := make([]string, 0, len(m.StancesByAgent))
		for k := range m.StancesByAgent {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s: %s\n", k, m.StancesByAgent[k])
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To apply these findings:")
	fmt.Fprintf(w, "  entire investigate fix %s\n", m.RunID)
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
