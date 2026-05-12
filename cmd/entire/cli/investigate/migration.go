// Package investigate — see env.go for package-level rationale.
//
// migration.go moves an investigate config from .entire/settings.json
// (committed) to .entire/settings.local.json (worktree-local). Triggered
// on every `entire investigate` invocation while the legacy field
// exists; once moved, it self-extinguishes. Mirrors review/migration.go
// in shape and copy.
package investigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/entireio/cli/cmd/entire/cli/jsonutil"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/settings"
)

type projectInvestigateSettings struct {
	path string
	raw  map[string]json.RawMessage
	body json.RawMessage
}

// maybePromptInvestigateSettingsMigration runs the one-time interactive
// migration. If the project settings file has an "investigate" key, the
// user is asked whether to move it. Non-interactive callers receive a
// guidance line on stderr.
func maybePromptInvestigateSettingsMigration(
	ctx context.Context,
	out io.Writer,
	errOut io.Writer,
	canPrompt bool,
	promptYN func(context.Context, string, bool) (bool, error),
) error {
	project, ok, err := loadProjectInvestigateSettings(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if !canPrompt {
		fmt.Fprintln(errOut,
			"Investigate preferences are stored in project settings (.entire/settings.json). "+
				"Run `entire investigate --edit` interactively to move them to local preferences.")
		return nil
	}

	if promptYN == nil {
		return errors.New("migration: promptYN required for interactive prompt")
	}
	migrate, err := promptYN(ctx,
		"Investigate preferences are stored in project settings (.entire/settings.json). "+
			"Move them to local preferences (.entire/settings.local.json) now?", false)
	if err != nil {
		return fmt.Errorf("investigate settings migration prompt: %w", err)
	}
	if !migrate {
		return nil
	}

	if err := migrateProjectInvestigateSettings(ctx, project); err != nil {
		return err
	}
	fmt.Fprintln(out, "Moved investigate preferences from project settings to local preferences.")
	return nil
}

func loadProjectInvestigateSettings(ctx context.Context) (*projectInvestigateSettings, bool, error) {
	path, err := paths.AbsPath(ctx, settings.EntireSettingsFile)
	if err != nil {
		path = settings.EntireSettingsFile
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is resolved from repo settings
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read project settings: %w", err)
	}

	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false, fmt.Errorf("parse project settings: %w", err)
	}

	body, ok := raw["investigate"]
	if !ok || isJSONNull(body) {
		return nil, false, nil
	}
	return &projectInvestigateSettings{path: path, raw: raw, body: body}, true, nil
}

func migrateProjectInvestigateSettings(ctx context.Context, project *projectInvestigateSettings) error {
	if project == nil {
		return nil
	}

	localPath, err := paths.AbsPath(ctx, settings.EntireSettingsLocalFile)
	if err != nil {
		localPath = settings.EntireSettingsLocalFile
	}

	localRaw := map[string]json.RawMessage{}
	if data, readErr := os.ReadFile(localPath); readErr == nil { //nolint:gosec // path is from AbsPath
		if err := json.Unmarshal(data, &localRaw); err != nil {
			return fmt.Errorf("parse local settings during migration: %w", err)
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read local settings during migration: %w", readErr)
	}

	if _, exists := localRaw["investigate"]; !exists {
		localRaw["investigate"] = project.body
		localData, err := jsonutil.MarshalIndentWithNewline(localRaw, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal local settings: %w", err)
		}
		//nolint:gosec // G306: settings file is config, not secrets; 0o644 is appropriate
		if err := os.WriteFile(localPath, localData, 0o644); err != nil {
			return fmt.Errorf("write local settings: %w", err)
		}
	}

	delete(project.raw, "investigate")
	data, err := jsonutil.MarshalIndentWithNewline(project.raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project settings: %w", err)
	}
	//nolint:gosec // G306
	if err := os.WriteFile(project.path, data, 0o644); err != nil {
		return fmt.Errorf("write project settings: %w", err)
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
