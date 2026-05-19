// Package uiform builds huh forms wired to Entire's standard theme and
// accessibility behavior. Centralising this keeps the picker UI consistent
// across cli, review, and investigate without each package re-implementing
// the same Theme()+WithAccessible() recipe and risking drift.
package uiform

import (
	"os"

	"charm.land/huh/v2"
)

// IsAccessibleMode reports whether accessibility mode is enabled via the
// ACCESSIBLE environment variable. Set ACCESSIBLE=1 (or any non-empty
// value) to enable simpler prompts that work better with screen readers.
func IsAccessibleMode() bool {
	return os.Getenv("ACCESSIBLE") != ""
}

// Theme returns Entire's standard huh theme.
//
//nolint:ireturn // huh.Theme is an interface in v2
func Theme() huh.Theme {
	return huh.ThemeFunc(huh.ThemeDracula)
}

// New creates a huh form with the standard theme, switching to accessible
// mode when ACCESSIBLE is set. WithAccessible is only available on forms
// (not individual fields), so wrap confirmations and other prompts in a
// form to opt into accessibility.
func New(groups ...*huh.Group) *huh.Form {
	form := huh.NewForm(groups...).WithTheme(Theme())
	if IsAccessibleMode() {
		form = form.WithAccessible(true)
	}
	return form
}
