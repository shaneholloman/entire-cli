package vercelconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/entireio/cli/cmd/entire/cli/jsonutil"
	"github.com/entireio/cli/cmd/entire/cli/settings"
)

const (
	BranchPattern = "entire/**"
	FileName      = "vercel.json"
)

var (
	cachedSettingsMu sync.RWMutex
	cachedSettings   *settings.EntireSettings
)

var errSettingsNotInitialized = errors.New("vercel settings cache not initialized")

// InitSettings loads repository settings for the current command context and
// stores them in a small package cache.
func InitSettings(ctx context.Context) error {
	cachedSettingsMu.RLock()
	if cachedSettings != nil {
		cachedSettingsMu.RUnlock()
		return nil
	}
	cachedSettingsMu.RUnlock()

	s, err := settings.Load(ctx)
	if err != nil {
		return err
	}

	cachedSettingsMu.Lock()
	cachedSettings = s
	cachedSettingsMu.Unlock()

	return nil
}

// CachedSettings returns the most recently initialized repository settings.
func CachedSettings() (*settings.EntireSettings, error) {
	cachedSettingsMu.RLock()
	defer cachedSettingsMu.RUnlock()
	if cachedSettings == nil {
		return nil, errSettingsNotInitialized
	}
	return cachedSettings, nil
}

// ResetSettingsCache clears the cached settings.
// Primarily intended for tests that exercise multiple repositories in one process.
func ResetSettingsCache() {
	cachedSettingsMu.Lock()
	cachedSettings = nil
	cachedSettingsMu.Unlock()
}

// Load reads an existing Vercel config file if present.
func Load(path string, exists bool) (map[string]any, bool, error) {
	if !exists {
		return make(map[string]any), false, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", FileName, err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if config == nil {
		config = make(map[string]any)
	}

	return config, DeploymentDisabled(config), nil
}

// DeploymentDisabled reports whether Entire branches are disabled in the config.
func DeploymentDisabled(config map[string]any) bool {
	gitConfig, ok := config["git"].(map[string]any)
	if !ok {
		return false
	}
	deploymentEnabled, ok := gitConfig["deploymentEnabled"].(map[string]any)
	if !ok {
		return false
	}
	enabled, ok := deploymentEnabled[BranchPattern].(bool)
	return ok && !enabled
}

// MergeDeploymentDisabled sets deploymentEnabled["entire/**"] = false while preserving other fields.
func MergeDeploymentDisabled(config map[string]any) {
	gitConfig, ok := config["git"].(map[string]any)
	if !ok {
		gitConfig = make(map[string]any)
		config["git"] = gitConfig
	}

	deploymentEnabled, ok := gitConfig["deploymentEnabled"].(map[string]any)
	if !ok {
		deploymentEnabled = make(map[string]any)
		gitConfig["deploymentEnabled"] = deploymentEnabled
	}

	deploymentEnabled[BranchPattern] = false
}

// Marshal formats a Vercel config with a trailing newline.
func Marshal(config map[string]any) ([]byte, error) {
	output, err := jsonutil.MarshalIndentWithNewline(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", FileName, err)
	}
	return output, nil
}
