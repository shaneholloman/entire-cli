package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	"github.com/entireio/cli/cmd/entire/cli/agent/types"
	"github.com/entireio/cli/cmd/entire/cli/logging"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/settings"
	"github.com/entireio/cli/cmd/entire/cli/summarize"

	"github.com/charmbracelet/huh"
)

var (
	loadSummarySettings         = LoadEntireSettings
	loadSummarySettingsFromFile = settings.LoadFromFile
	saveProjectSummarySettings  = SaveEntireSettings
	saveLocalSummarySettings    = SaveEntireSettingsLocal
	getSummaryAgent             = agent.Get
	listRegisteredAgents        = agent.List
)

type checkpointSummaryProvider struct {
	Name         types.AgentName
	DisplayName  string
	Model        string
	DisplayModel string
	Generator    summarize.Generator
}

func resolveCheckpointSummaryProvider(ctx context.Context, w io.Writer) (*checkpointSummaryProvider, error) {
	s, err := loadSummarySettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading settings: %w", err)
	}

	if s.SummaryGeneration != nil && s.SummaryGeneration.Provider != "" {
		return buildCheckpointSummaryProvider(types.AgentName(s.SummaryGeneration.Provider), s.SummaryGeneration.Model)
	}

	candidates := listEnabledSummaryProviders(ctx)

	switch len(candidates) {
	case 0:
		logging.Info(ctx, "no summary-capable agents installed, falling back to Claude Code")
		return buildCheckpointSummaryProvider(agent.AgentNameClaudeCode, "")
	case 1:
		provider, err := buildCheckpointSummaryProvider(candidates[0].Name, "")
		if err != nil {
			return nil, err
		}
		if saveErr := persistSummaryProviderSelection(ctx, provider.Name, provider.Model); saveErr != nil {
			logging.Warn(ctx, "failed to save summary provider selection, continuing without persistence",
				"error", saveErr.Error())
			fmt.Fprintf(w, "Warning: could not save provider selection: %v\nUse `entire configure --summarize-provider %s` to set it manually.\n", saveErr, provider.Name)
		}
		return provider, nil
	default:
		if !canPromptInteractively() {
			logging.Info(ctx, "non-interactive mode with multiple summary providers, falling back to Claude Code")
			return buildCheckpointSummaryProvider(agent.AgentNameClaudeCode, "")
		}

		selected, err := promptForSummaryProvider(candidates)
		if err != nil {
			return nil, err
		}

		provider, err := buildCheckpointSummaryProvider(selected, "")
		if err != nil {
			return nil, err
		}
		if saveErr := persistSummaryProviderSelection(ctx, provider.Name, provider.Model); saveErr != nil {
			logging.Warn(ctx, "failed to save summary provider selection, continuing without persistence",
				"error", saveErr.Error())
			fmt.Fprintf(w, "Warning: could not save provider selection: %v\nUse `entire configure --summarize-provider %s` to set it manually.\n", saveErr, provider.Name)
		}
		fmt.Fprintf(w, "Using %s for summary generation.\n", provider.DisplayName)
		return provider, nil
	}
}

func listEnabledSummaryProviders(ctx context.Context) []checkpointSummaryProvider {
	registered := listRegisteredAgents()
	providers := make([]checkpointSummaryProvider, 0, len(registered))
	for _, name := range registered {
		ag, err := getSummaryAgent(name)
		if err != nil {
			continue
		}
		if _, ok := agent.AsTextGenerator(ag); !ok {
			continue
		}
		present, err := ag.DetectPresence(ctx)
		if err != nil {
			// Log at Debug so a broken install (e.g., permission error on the
			// agent's config dir) doesn't silently masquerade as "not installed"
			// without any trace.
			logging.Debug(ctx, "summary provider presence detection failed, skipping",
				"agent", string(name), "error", err.Error())
			continue
		}
		if !present {
			continue
		}
		providers = append(providers, checkpointSummaryProvider{
			Name:        name,
			DisplayName: string(ag.Type()),
		})
	}
	return providers
}

func promptForSummaryProvider(providers []checkpointSummaryProvider) (types.AgentName, error) {
	options := make([]huh.Option[string], 0, len(providers))
	for _, provider := range providers {
		options = append(options, huh.NewOption(provider.DisplayName, string(provider.Name)))
	}

	var selected string
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Choose a summary provider").
				Description("This choice will be saved. Use `entire configure` to change it later.").
				Options(options...).
				Value(&selected),
		),
	)
	if err := form.Run(); err != nil {
		return "", fmt.Errorf("summary provider selection cancelled: %w", err)
	}

	return types.AgentName(selected), nil
}

func buildCheckpointSummaryProvider(name types.AgentName, model string) (*checkpointSummaryProvider, error) {
	ag, err := getSummaryAgent(name)
	if err != nil {
		return nil, fmt.Errorf("loading summary provider %s: %w", name, err)
	}

	textGenerator, ok := agent.AsTextGenerator(ag)
	if !ok {
		return nil, fmt.Errorf("agent %s does not support summary generation", name)
	}

	effectiveModel := summarize.ResolveModel(name, model)
	displayModel := effectiveModel
	if displayModel == "" {
		displayModel = "provider default"
	}

	return &checkpointSummaryProvider{
		Name:         name,
		DisplayName:  string(ag.Type()),
		Model:        effectiveModel,
		DisplayModel: displayModel,
		Generator: &summarize.TextGeneratorAdapter{
			TextGenerator: textGenerator,
			Model:         effectiveModel,
		},
	}, nil
}

func validateSummaryProvider(provider string) error {
	ag, err := getSummaryAgent(types.AgentName(provider))
	if err != nil {
		return fmt.Errorf("unknown summary provider %q: %w", provider, err)
	}
	if _, ok := agent.AsTextGenerator(ag); !ok {
		return fmt.Errorf("agent %q does not support summary generation", provider)
	}
	return nil
}

func persistSummaryProviderSelection(ctx context.Context, provider types.AgentName, model string) error {
	targetFile, _ := settingsTargetFile(ctx, false, false)
	targetFileAbs, err := paths.AbsPath(ctx, targetFile)
	if err != nil {
		targetFileAbs = targetFile
	}

	s, err := loadSummarySettingsFromFile(targetFileAbs)
	if err != nil {
		return fmt.Errorf("loading settings for update: %w", err)
	}
	if s.SummaryGeneration == nil {
		s.SummaryGeneration = &settings.SummaryGenerationSettings{}
	}
	if s.SummaryGeneration.Provider != "" && s.SummaryGeneration.Provider != string(provider) && model == "" {
		s.SummaryGeneration.Model = ""
	}
	s.SummaryGeneration.Provider = string(provider)
	if model != "" {
		s.SummaryGeneration.Model = model
	}

	if targetFile == settings.EntireSettingsLocalFile {
		if err := saveLocalSummarySettings(ctx, s); err != nil {
			return fmt.Errorf("saving summary provider selection: %w", err)
		}
		return nil
	}
	if err := saveProjectSummarySettings(ctx, s); err != nil {
		return fmt.Errorf("saving summary provider selection: %w", err)
	}
	return nil
}

func formatSummaryProviderDetails(provider *checkpointSummaryProvider) string {
	if provider == nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Provider: %s\n", provider.DisplayName)
	fmt.Fprintf(&b, "Model: %s\n", provider.DisplayModel)
	return b.String()
}
