// Package investigate — see env.go for package-level rationale.
//
// picker.go implements the first-run config picker for `entire
// investigate`. The picker shows a multi-select of agents that are both
// installed (hooks present) and launchable (a non-nil Spawner exists for
// them), then prompts for max-turns / quorum, and returns an
// InvestigateConfig the caller can persist.
package investigate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"

	"charm.land/huh/v2"

	"github.com/entireio/cli/cmd/entire/cli/agent/spawn"
	"github.com/entireio/cli/cmd/entire/cli/agent/types"
	"github.com/entireio/cli/cmd/entire/cli/settings"
)

// AgentChoice is one row in the investigate picker. Name is the agent
// registry key (used for spawning); Label is the picker-visible string.
// Exported so tests in investigate_test can drive the picker form fn
// directly.
type AgentChoice struct {
	Name  string
	Label string
}

// newAccessibleForm creates a huh form with accessibility mode enabled
// when the ACCESSIBLE env var is set. Mirrors cli.NewAccessibleForm
// without requiring an import of the cli package (which would be
// circular).
func newAccessibleForm(groups ...*huh.Group) *huh.Form {
	form := huh.NewForm(groups...).WithTheme(huh.ThemeFunc(huh.ThemeDracula))
	if os.Getenv("ACCESSIBLE") != "" {
		form = form.WithAccessible(true)
	}
	return form
}

// ConfirmFirstRunSetup prints a banner framing the picker as first-run
// setup (rather than the investigation itself) and waits for the user to
// confirm. Mirrors review.ConfirmFirstRunSetup.
func ConfirmFirstRunSetup(ctx context.Context, out io.Writer) bool {
	fmt.Fprintln(out, "No investigate config found — let's set one up first.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "You'll pick which agents take turns during an investigation, and the")
	fmt.Fprintln(out, "max-turns / quorum the loop should use. The selection is saved to")
	fmt.Fprintln(out, ".entire/settings.json; edit later with `entire investigate --edit`.")
	fmt.Fprintln(out, "After setup, the investigation will run with your selection.")
	fmt.Fprintln(out)

	proceed := true
	form := newAccessibleForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Set up investigate now?").
			Affirmative("Yes").
			Negative("Cancel").
			Value(&proceed),
	))
	if err := form.RunWithContext(ctx); err != nil {
		fmt.Fprintln(out, "Setup cancelled.")
		return false
	}
	if !proceed {
		fmt.Fprintln(out, "Setup cancelled.")
	}
	return proceed
}

// eligibleAgentsForInvestigate filters and sorts the eligible-agent list
// for picker display. An agent is eligible iff it has a non-nil Spawner
// (i.e. is launchable by the CLI) AND has hooks installed in the current
// repo.
func eligibleAgentsForInvestigate(_ context.Context, spawnerFor func(string) spawn.Spawner, hookInstalled []types.AgentName) []AgentChoice {
	if spawnerFor == nil {
		return nil
	}
	out := make([]AgentChoice, 0, len(hookInstalled))
	for _, n := range hookInstalled {
		name := string(n)
		if spawnerFor(name) == nil {
			continue
		}
		out = append(out, AgentChoice{Name: name, Label: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// pickerFormFn renders the multi-select + max-turns + quorum form. The
// production implementation uses huh; tests inject a stub via
// pickerFormOverride below.
type pickerFormFn func(ctx context.Context, eligible []AgentChoice, picks *[]string, maxTurns, quorum *int) error

// pickerFormOverride, when non-nil, replaces the production huh form
// inside RunInvestigateConfigPicker. Tests set this via
// SetPickerFormFnForTest to drive the picker headlessly.
var pickerFormOverride pickerFormFn

// SetPickerFormFnForTest swaps the picker form function. Returns a
// cleanup function the caller must defer to restore the previous value.
// Intended for tests only.
func SetPickerFormFnForTest(fn pickerFormFn) func() {
	prev := pickerFormOverride
	pickerFormOverride = fn
	return func() { pickerFormOverride = prev }
}

// RunInvestigateConfigPicker shows a multi-select of eligible agents and
// prompts for max-turns / quorum. Returns a populated InvestigateConfig
// the caller can persist via settings.Save.
//
// Eligibility: agent has a non-nil Spawner AND has hooks installed.
// Non-spawnable agents (cursor, opencode, factoryai-droid, copilot-cli)
// are filtered out at the SpawnerFor check.
func RunInvestigateConfigPicker(
	ctx context.Context,
	out io.Writer,
	spawnerFor func(agentName string) spawn.Spawner,
	getAgentsWithHooksInstalled func(ctx context.Context) []types.AgentName,
) (*settings.InvestigateConfig, error) {
	if getAgentsWithHooksInstalled == nil {
		return nil, errors.New("RunInvestigateConfigPicker: GetAgentsWithHooksInstalled not wired")
	}
	if spawnerFor == nil {
		return nil, errors.New("RunInvestigateConfigPicker: SpawnerFor not wired")
	}

	installed := getAgentsWithHooksInstalled(ctx)
	eligible := eligibleAgentsForInvestigate(ctx, spawnerFor, installed)
	if len(eligible) == 0 {
		return nil, errors.New(
			"no launchable agents with hooks installed; " +
				"run `entire configure --agent <name>` for one of: " +
				"claude-code, codex, gemini-cli",
		)
	}

	// Defaults: select all eligible agents, MaxTurns=3, Quorum=0 (== all).
	picks := make([]string, len(eligible))
	for i, c := range eligible {
		picks[i] = c.Name
	}
	maxTurns := 3
	quorum := 0

	fmt.Fprintf(out, "Configuring investigate with %d eligible agent(s).\n", len(eligible))
	fmt.Fprintln(out, "(Space to toggle, enter to confirm.)")
	fmt.Fprintln(out)

	formFn := pickerFormOverride
	if formFn == nil {
		formFn = runInvestigatePickerForm
	}
	if err := formFn(ctx, eligible, &picks, &maxTurns, &quorum); err != nil {
		return nil, fmt.Errorf("investigate picker: %w", err)
	}
	if len(picks) == 0 {
		return nil, errors.New("no agents selected")
	}
	if maxTurns < 0 {
		return nil, errors.New("max-turns must be non-negative")
	}
	if quorum < 0 {
		return nil, errors.New("quorum must be non-negative")
	}
	if quorum > len(picks) {
		return nil, fmt.Errorf("quorum (%d) cannot exceed agent count (%d)", quorum, len(picks))
	}

	cfg := &settings.InvestigateConfig{
		Agents:   picks,
		MaxTurns: maxTurns,
		Quorum:   quorum,
	}
	fmt.Fprintln(out, "Saved investigate config to .entire/settings.json. Edit directly or run `entire investigate --edit`.")
	return cfg, nil
}

// runInvestigatePickerForm renders the production huh picker form.
func runInvestigatePickerForm(ctx context.Context, eligible []AgentChoice, picks *[]string, maxTurns, quorum *int) error {
	options := make([]huh.Option[string], 0, len(eligible))
	preselected := map[string]struct{}{}
	if picks != nil {
		for _, p := range *picks {
			preselected[p] = struct{}{}
		}
	}
	for _, c := range eligible {
		opt := huh.NewOption(c.Label, c.Name)
		if _, ok := preselected[c.Name]; ok {
			opt = opt.Selected(true)
		}
		options = append(options, opt)
	}

	maxTurnsStr := strconv.Itoa(*maxTurns)
	quorumStr := strconv.Itoa(*quorum)

	form := newAccessibleForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Agents (round-robin)").
			Description("Selected agents take turns during the investigation.").
			Options(options...).
			Height(min(len(options)+2, 12)).
			Value(picks),
		huh.NewInput().
			Title("Max turns per agent").
			Description("Per-agent turn budget. Defaults to 3.").
			Value(&maxTurnsStr),
		huh.NewInput().
			Title("Quorum").
			Description("Approve stances needed to terminate. 0 = all agents must approve.").
			Value(&quorumStr),
	))
	if err := form.RunWithContext(ctx); err != nil {
		return fmt.Errorf("picker form: %w", err)
	}
	mt, err := strconv.Atoi(maxTurnsStr)
	if err != nil {
		return fmt.Errorf("max-turns: %w", err)
	}
	q, err := strconv.Atoi(quorumStr)
	if err != nil {
		return fmt.Errorf("quorum: %w", err)
	}
	*maxTurns = mt
	*quorum = q
	return nil
}
