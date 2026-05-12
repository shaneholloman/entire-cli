// Package investigate — see env.go for package-level rationale.
//
// tui_text.go is a verbatim port of the same helpers in review/tui_text.go.
// We keep a private copy here so the investigate TUI does not depend on the
// review package, mirroring how review keeps these helpers private.
package investigate

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

func stripANSI(s string) string {
	return ansi.Strip(s)
}

func sanitizeDisplayText(s string) string {
	stripped := stripANSI(s)
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return ' '
		case '\r':
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, stripped)
}

func padDisplayWidth(s string, width int) string {
	s = truncateDisplayWidth(s, width)
	remaining := width - ansi.StringWidth(s)
	if remaining <= 0 {
		return s
	}
	return s + strings.Repeat(" ", remaining)
}

func truncateDisplayWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, width, "…")
}
