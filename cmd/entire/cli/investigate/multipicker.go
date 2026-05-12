// Package investigate — see env.go for package-level rationale.
//
// multipicker.go provides spawn-time agent multi-selection and per-run
// prompt collection for multi-agent investigation runs. When 2+ eligible
// agents are configured AND the user has not passed --agents, the
// dispatch logic in cmd.go calls PickInvestigateAgents to let the user
// choose a subset and optionally add a one-off preamble without editing
// settings. Mirrors review/multipicker.go.
package investigate

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"charm.land/huh/v2"
)

// PickedInvestigate is the result of PickInvestigateAgents: the agents
// the user selected for this run, plus an optional per-run prompt that
// callers should append to the configured AlwaysPrompt for the duration
// of this run only.
type PickedInvestigate struct {
	Names  []string
	PerRun string
}

// ErrInvestigatePickerCancelled is returned when the user aborts the
// multi-select.
var ErrInvestigatePickerCancelled = errors.New("investigate agent picker cancelled")

// ErrInvestigateNoAgentsSelected is returned when the user unchecks all
// agents.
var ErrInvestigateNoAgentsSelected = errors.New("no agents selected for investigation")

// PickInvestigateAgents shows a multi-select form populated from eligible
// (the agents that are both configured AND have a launchable Spawner),
// pre-checks all of them, and returns the user's selection plus an
// optional per-run prompt.
//
// Requires len(eligible) >= 2.
func PickInvestigateAgents(ctx context.Context, eligible []AgentChoice) (PickedInvestigate, error) {
	if len(eligible) < 2 {
		return PickedInvestigate{}, fmt.Errorf("PickInvestigateAgents requires at least 2 eligible agents, got %d", len(eligible))
	}
	if ctx.Err() != nil {
		return PickedInvestigate{}, ErrInvestigatePickerCancelled
	}

	sorted := make([]AgentChoice, len(eligible))
	copy(sorted, eligible)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	options := make([]huh.Option[string], 0, len(sorted))
	for _, c := range sorted {
		label := c.Label
		if label == "" {
			label = c.Name
		}
		options = append(options, huh.NewOption(label, c.Name).Selected(true))
	}

	var picked []string
	multiForm := newAccessibleForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Which agents should run this investigation?").
			Options(options...).
			Height(len(options) + 1).
			Value(&picked),
	))
	if err := multiForm.RunWithContext(ctx); err != nil {
		return PickedInvestigate{}, ErrInvestigatePickerCancelled
	}

	if len(picked) == 0 {
		return PickedInvestigate{}, ErrInvestigateNoAgentsSelected
	}
	sort.Strings(picked)

	var perRun string
	promptForm := newAccessibleForm(huh.NewGroup(
		huh.NewText().
			Title("Optional per-run prompt").
			Description("e.g. 'focus on the auth boundary' — appended to the always_prompt for this run only. Leave blank to skip.").
			Value(&perRun),
	))
	if err := promptForm.RunWithContext(ctx); err != nil {
		return PickedInvestigate{}, ErrInvestigatePickerCancelled
	}

	return PickedInvestigate{Names: picked, PerRun: perRun}, nil
}
