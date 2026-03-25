package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/jsonutil"
	"github.com/entireio/cli/cmd/entire/cli/paths"
)

// HooksFileName is the hooks config file used by Codex.
const HooksFileName = "hooks.json"

// entireHookPrefixes identifies Entire hook commands.
var entireHookPrefixes = []string{
	"entire ",
	`go run "$(git rev-parse --show-toplevel)"/cmd/entire/main.go `,
}

// InstallHooks installs Codex hooks in .codex/hooks.json.
func (c *CodexAgent) InstallHooks(ctx context.Context, localDev bool, force bool) (int, error) {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		repoRoot, err = os.Getwd() //nolint:forbidigo // Intentional fallback when WorktreeRoot() fails (tests)
		if err != nil {
			return 0, fmt.Errorf("failed to get current directory: %w", err)
		}
	}

	hooksPath := filepath.Join(repoRoot, ".codex", HooksFileName)

	// Read existing hooks.json if present
	var rawHooks map[string]json.RawMessage
	existingData, readErr := os.ReadFile(hooksPath) //nolint:gosec // path constructed from repo root
	if readErr == nil {
		var hooksFile map[string]json.RawMessage
		if err := json.Unmarshal(existingData, &hooksFile); err != nil {
			return 0, fmt.Errorf("failed to parse existing hooks.json: %w", err)
		}
		if hooksRaw, ok := hooksFile["hooks"]; ok {
			if err := json.Unmarshal(hooksRaw, &rawHooks); err != nil {
				return 0, fmt.Errorf("failed to parse hooks in hooks.json: %w", err)
			}
		}
	}

	if rawHooks == nil {
		rawHooks = make(map[string]json.RawMessage)
	}

	// Parse event types we manage
	var sessionStart, userPromptSubmit, stop []MatcherGroup
	parseHookType(rawHooks, "SessionStart", &sessionStart)
	parseHookType(rawHooks, "UserPromptSubmit", &userPromptSubmit)
	parseHookType(rawHooks, "Stop", &stop)

	if force {
		sessionStart = removeEntireHooks(sessionStart)
		userPromptSubmit = removeEntireHooks(userPromptSubmit)
		stop = removeEntireHooks(stop)
	}

	// Build hook commands
	var cmdPrefix string
	if localDev {
		cmdPrefix = `go run "$(git rev-parse --show-toplevel)"/cmd/entire/main.go hooks codex `
	} else {
		cmdPrefix = "entire hooks codex "
	}
	sessionStartCmd := cmdPrefix + "session-start"
	userPromptSubmitCmd := cmdPrefix + "user-prompt-submit"
	stopCmd := cmdPrefix + "stop"

	count := 0

	if !hookCommandExists(sessionStart, sessionStartCmd) {
		sessionStart = addHook(sessionStart, sessionStartCmd)
		count++
	}
	if !hookCommandExists(userPromptSubmit, userPromptSubmitCmd) {
		userPromptSubmit = addHook(userPromptSubmit, userPromptSubmitCmd)
		count++
	}
	if !hookCommandExists(stop, stopCmd) {
		stop = addHook(stop, stopCmd)
		count++
	}

	if count == 0 {
		return 0, nil
	}

	// Marshal modified types back
	marshalHookType(rawHooks, "SessionStart", sessionStart)
	marshalHookType(rawHooks, "UserPromptSubmit", userPromptSubmit)
	marshalHookType(rawHooks, "Stop", stop)

	// Build the top-level structure
	topLevel := map[string]json.RawMessage{}
	hooksJSON, err := json.Marshal(rawHooks)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal hooks: %w", err)
	}
	topLevel["hooks"] = hooksJSON

	// Write to file
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		return 0, fmt.Errorf("failed to create .codex directory: %w", err)
	}

	output, err := jsonutil.MarshalIndentWithNewline(topLevel, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("failed to marshal hooks.json: %w", err)
	}

	if err := os.WriteFile(hooksPath, output, 0o600); err != nil {
		return 0, fmt.Errorf("failed to write hooks.json: %w", err)
	}

	// Enable the codex_hooks feature in the project-level .codex/config.toml.
	// This keeps the feature flag per-repo rather than global.
	if err := ensureProjectFeatureEnabled(repoRoot); err != nil {
		return count, fmt.Errorf("failed to enable codex_hooks feature: %w", err)
	}

	// Trust this project in the user-level ~/.codex/config.toml so Codex
	// actually loads the project-level .codex/ config layer (hooks.json + config.toml).
	// Without trust, Codex silently disables the entire project layer.
	if err := ensureProjectTrusted(repoRoot); err != nil {
		return count, fmt.Errorf("failed to trust project: %w", err)
	}

	return count, nil
}

// UninstallHooks removes Entire hooks from Codex hooks.json.
func (c *CodexAgent) UninstallHooks(ctx context.Context) error {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		repoRoot = "."
	}

	hooksPath := filepath.Join(repoRoot, ".codex", HooksFileName)
	data, err := os.ReadFile(hooksPath) //nolint:gosec // path constructed from repo root
	if err != nil {
		return nil //nolint:nilerr // No hooks.json means nothing to uninstall
	}

	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		return fmt.Errorf("failed to parse hooks.json: %w", err)
	}

	var rawHooks map[string]json.RawMessage
	if hooksRaw, ok := topLevel["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &rawHooks); err != nil {
			return fmt.Errorf("failed to parse hooks: %w", err)
		}
	}
	if rawHooks == nil {
		return nil
	}

	var sessionStart, userPromptSubmit, stop []MatcherGroup
	parseHookType(rawHooks, "SessionStart", &sessionStart)
	parseHookType(rawHooks, "UserPromptSubmit", &userPromptSubmit)
	parseHookType(rawHooks, "Stop", &stop)

	sessionStart = removeEntireHooks(sessionStart)
	userPromptSubmit = removeEntireHooks(userPromptSubmit)
	stop = removeEntireHooks(stop)

	marshalHookType(rawHooks, "SessionStart", sessionStart)
	marshalHookType(rawHooks, "UserPromptSubmit", userPromptSubmit)
	marshalHookType(rawHooks, "Stop", stop)

	if len(rawHooks) > 0 {
		hooksJSON, err := json.Marshal(rawHooks)
		if err != nil {
			return fmt.Errorf("failed to marshal hooks: %w", err)
		}
		topLevel["hooks"] = hooksJSON
	} else {
		delete(topLevel, "hooks")
	}

	output, err := jsonutil.MarshalIndentWithNewline(topLevel, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal hooks.json: %w", err)
	}
	if err := os.WriteFile(hooksPath, output, 0o600); err != nil {
		return fmt.Errorf("failed to write hooks.json: %w", err)
	}
	return nil
}

// AreHooksInstalled checks if Entire hooks are installed in Codex hooks.json.
func (c *CodexAgent) AreHooksInstalled(ctx context.Context) bool {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		repoRoot = "."
	}

	hooksPath := filepath.Join(repoRoot, ".codex", HooksFileName)
	data, err := os.ReadFile(hooksPath) //nolint:gosec // path constructed from repo root
	if err != nil {
		return false
	}

	var hooksFile HooksFile
	if err := json.Unmarshal(data, &hooksFile); err != nil {
		return false
	}

	return hookCommandExists(hooksFile.Hooks.Stop, "entire hooks codex stop") ||
		hookCommandExists(hooksFile.Hooks.Stop, "go run ${CLAUDE_PROJECT_DIR}/cmd/entire/main.go hooks codex stop")
}

// --- Helpers ---

func parseHookType(rawHooks map[string]json.RawMessage, hookType string, target *[]MatcherGroup) {
	if data, ok := rawHooks[hookType]; ok {
		//nolint:errcheck,gosec // Intentionally ignoring parse errors
		json.Unmarshal(data, target)
	}
}

func marshalHookType(rawHooks map[string]json.RawMessage, hookType string, groups []MatcherGroup) {
	if len(groups) == 0 {
		delete(rawHooks, hookType)
		return
	}
	data, err := json.Marshal(groups)
	if err != nil {
		return
	}
	rawHooks[hookType] = data
}

func hookCommandExists(groups []MatcherGroup, command string) bool {
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == command {
				return true
			}
		}
	}
	return false
}

func addHook(groups []MatcherGroup, command string) []MatcherGroup {
	entry := HookEntry{
		Type:    "command",
		Command: command,
		Timeout: 30,
	}

	// Add to an existing group with null matcher, or create a new one
	for i, group := range groups {
		if group.Matcher == nil {
			groups[i].Hooks = append(groups[i].Hooks, entry)
			return groups
		}
	}
	return append(groups, MatcherGroup{
		Matcher: nil,
		Hooks:   []HookEntry{entry},
	})
}

func isEntireHook(command string) bool {
	for _, prefix := range entireHookPrefixes {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

func removeEntireHooks(groups []MatcherGroup) []MatcherGroup {
	result := make([]MatcherGroup, 0, len(groups))
	for _, group := range groups {
		filtered := make([]HookEntry, 0, len(group.Hooks))
		for _, hook := range group.Hooks {
			if !isEntireHook(hook.Command) {
				filtered = append(filtered, hook)
			}
		}
		if len(filtered) > 0 {
			group.Hooks = filtered
			result = append(result, group)
		}
	}
	return result
}

// configFileName is the Codex config file name.
const configFileName = "config.toml"

// featureLine is the TOML line that enables the codex_hooks feature.
const featureLine = "codex_hooks = true"

// ensureProjectFeatureEnabled writes features.codex_hooks = true to the
// project-level .codex/config.toml. This keeps the feature flag per-repo.
func ensureProjectFeatureEnabled(repoRoot string) error {
	configPath := filepath.Join(repoRoot, ".codex", configFileName)

	data, err := os.ReadFile(configPath) //nolint:gosec // path constructed from repo root
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config.toml: %w", err)
	}

	content := string(data)
	if strings.Contains(content, featureLine) {
		return nil
	}

	if strings.Contains(content, "[features]") {
		content = strings.Replace(content, "[features]", "[features]\n"+featureLine, 1)
	} else {
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n[features]\n" + featureLine + "\n"
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		return fmt.Errorf("failed to create .codex directory: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil { //nolint:gosec // path constructed from repo root
		return fmt.Errorf("failed to write config.toml: %w", err)
	}
	return nil
}

// ensureProjectTrusted adds a trust entry for the repo in the user-level
// ~/.codex/config.toml so Codex loads the project's .codex/ config layer.
func ensureProjectTrusted(repoRoot string) error {
	codexHome, err := resolveCodexHome()
	if err != nil {
		return err
	}

	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path: %w", err)
	}

	configPath := filepath.Join(codexHome, configFileName)

	data, err := os.ReadFile(configPath) //nolint:gosec // path in user home directory
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config.toml: %w", err)
	}

	content := string(data)

	trustSection := fmt.Sprintf("[projects.%q]", absRoot)
	if strings.Contains(content, trustSection) {
		return nil // already has an entry for this project
	}

	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + trustSection + "\n" + `trust_level = "trusted"` + "\n"

	if err := os.MkdirAll(codexHome, 0o750); err != nil {
		return fmt.Errorf("failed to create codex home directory: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil { //nolint:gosec // path in user home directory
		return fmt.Errorf("failed to write config.toml: %w", err)
	}
	return nil
}
