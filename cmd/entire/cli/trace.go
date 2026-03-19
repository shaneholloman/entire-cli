package cli

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// traceStep represents a single timed step within a trace span.
// Group steps (from nested spans) have SubSteps with 0-based iteration numbering.
type traceStep struct {
	Name       string
	DurationMs int64
	Error      bool
	SubSteps   []traceStep
}

// traceEntry represents a parsed performance trace log entry.
type traceEntry struct {
	Op         string
	DurationMs int64
	Error      bool
	Time       time.Time
	Steps      []traceStep
}

// parseTraceEntry parses a JSON log line into a traceEntry.
// Returns nil if the line is not valid JSON or is not a trace entry (msg != "perf").
func parseTraceEntry(line string) *traceEntry {
	// Cheap pre-filter: skip full JSON parse for lines that can't be perf entries.
	// Most lines in the shared log file are non-perf, so this avoids the
	// marshalling cost for the common reject path.
	if !strings.Contains(line, `"msg":"perf"`) {
		return nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil
	}

	// Verify msg == "perf" after full parse (the pre-filter could match substrings)
	var msg string
	if msgRaw, ok := raw["msg"]; !ok {
		return nil
	} else if err := json.Unmarshal(msgRaw, &msg); err != nil || msg != "perf" {
		return nil
	}

	entry := &traceEntry{}

	// Best-effort field extraction: missing or mistyped fields keep their
	// zero values rather than discarding the entire entry.
	if opRaw, ok := raw["op"]; ok {
		_ = json.Unmarshal(opRaw, &entry.Op) //nolint:errcheck // best-effort
	}
	if dRaw, ok := raw["duration_ms"]; ok {
		_ = json.Unmarshal(dRaw, &entry.DurationMs) //nolint:errcheck // best-effort
	}
	if errRaw, ok := raw["error"]; ok {
		_ = json.Unmarshal(errRaw, &entry.Error) //nolint:errcheck // best-effort
	}

	// Extract time
	if tRaw, ok := raw["time"]; ok {
		var ts string
		if err := json.Unmarshal(tRaw, &ts); err == nil {
			if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				entry.Time = parsed
			}
		}
	}

	// Extract steps by finding keys matching "steps.*_ms"
	stepDurations := make(map[string]int64)
	stepErrors := make(map[string]bool)

	for key, val := range raw {
		if strings.HasPrefix(key, "steps.") && strings.HasSuffix(key, "_ms") {
			name := strings.TrimPrefix(key, "steps.")
			name = strings.TrimSuffix(name, "_ms")

			var ms int64
			if err := json.Unmarshal(val, &ms); err == nil {
				stepDurations[name] = ms
			}
		} else if strings.HasPrefix(key, "steps.") && strings.HasSuffix(key, "_err") {
			name := strings.TrimPrefix(key, "steps.")
			name = strings.TrimSuffix(name, "_err")

			var errFlag bool
			if err := json.Unmarshal(val, &errFlag); err == nil {
				stepErrors[name] = errFlag
			}
		}
	}

	// Separate parent steps from sub-steps.
	// A key like "foo.0" is a sub-step of "foo" if "foo" also exists as a parent
	// and the last segment is a non-negative integer.
	subStepDurations := make(map[string]map[int]int64) // parent -> index -> ms
	subStepErrors := make(map[string]map[int]bool)     // parent -> index -> err
	parentStepDurations := make(map[string]int64)
	parentStepErrors := make(map[string]bool)

	for name, ms := range stepDurations {
		if parent, idx, ok := parseSubStepKey(name, stepDurations); ok {
			if subStepDurations[parent] == nil {
				subStepDurations[parent] = make(map[int]int64)
			}
			subStepDurations[parent][idx] = ms
			if stepErrors[name] {
				if subStepErrors[parent] == nil {
					subStepErrors[parent] = make(map[int]bool)
				}
				subStepErrors[parent][idx] = true
			}
		} else {
			parentStepDurations[name] = ms
			parentStepErrors[name] = stepErrors[name]
		}
	}

	// Build steps slice sorted alphabetically by name
	steps := make([]traceStep, 0, len(parentStepDurations))
	for name, ms := range parentStepDurations {
		step := traceStep{
			Name:       name,
			DurationMs: ms,
			Error:      parentStepErrors[name],
		}

		// Attach sub-steps if any, sorted by numeric index
		if subs, ok := subStepDurations[name]; ok {
			indices := make([]int, 0, len(subs))
			for idx := range subs {
				indices = append(indices, idx)
			}
			slices.Sort(indices)

			subList := make([]traceStep, 0, len(subs))
			for _, idx := range indices {
				subList = append(subList, traceStep{
					Name:       fmt.Sprintf("%s.%d", name, idx),
					DurationMs: subs[idx],
					Error:      subStepErrors[name][idx],
				})
			}
			step.SubSteps = subList
		}

		steps = append(steps, step)
	}
	slices.SortFunc(steps, func(a, b traceStep) int {
		return cmp.Compare(a.Name, b.Name)
	})

	entry.Steps = steps

	return entry
}

// parseSubStepKey checks if a step name like "foo.0" is a sub-step of "foo".
// Returns the parent name, index, and true if it is a sub-step.
// A name is a sub-step if: the last segment after the final "." is a non-negative
// integer AND the parent name exists in allSteps.
func parseSubStepKey(name string, allSteps map[string]int64) (string, int, bool) {
	lastDot := strings.LastIndex(name, ".")
	if lastDot < 0 {
		return "", 0, false
	}
	parent := name[:lastDot]
	suffix := name[lastDot+1:]
	idx, err := strconv.Atoi(suffix)
	if err != nil || idx < 0 {
		return "", 0, false
	}
	if _, exists := allSteps[parent]; !exists {
		return "", 0, false
	}
	return parent, idx, true
}

// collectTraceEntries reads a JSONL log file and returns the last N trace entries,
// ordered newest first. If hookFilter is non-empty, only entries with a matching
// Op field are included.
func collectTraceEntries(logFile string, last int, hookFilter string) ([]traceEntry, error) {
	f, err := os.Open(logFile) //nolint:gosec // logFile is a CLI-resolved path, not user-supplied input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	defer f.Close()

	var entries []traceEntry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024) // allow up to 1MB lines in shared log file
	for scanner.Scan() {
		entry := parseTraceEntry(scanner.Text())
		if entry == nil {
			continue
		}
		if hookFilter != "" && entry.Op != hookFilter {
			continue
		}
		entries = append(entries, *entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading log file: %w", err)
	}

	// Take the last N entries
	if len(entries) > last {
		entries = entries[len(entries)-last:]
	}

	// Reverse so newest entries are first
	slices.Reverse(entries)

	return entries, nil
}

// renderTraceEntries writes a formatted table of trace entries to w.
// If entries is empty, it prints a help message about enabling traces.
func renderTraceEntries(w io.Writer, entries []traceEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "No trace entries found.")
		fmt.Fprintln(w, `Traces are logged at DEBUG level. Make sure ENTIRE_LOG_LEVEL=DEBUG is set`)
		fmt.Fprintln(w, `in your shell profile, or set log_level to "DEBUG" in .entire/settings.json.`)
		return
	}

	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}

		// Header line: op  duration  [timestamp]
		header := fmt.Sprintf("%s  %dms", entry.Op, entry.DurationMs)
		if !entry.Time.IsZero() {
			header += "  " + entry.Time.Format(time.RFC3339)
		}
		fmt.Fprintln(w, header)
		fmt.Fprintln(w)

		if len(entry.Steps) == 0 {
			continue
		}

		// Compute max name display width (at least len("STEP")).
		// Sub-steps are indented 5 extra display columns relative to parent rows
		// ("    " + "├─ " = 7 display cols vs "  " = 2 display cols).
		const subExtraIndent = 5
		nameWidth := len("STEP")
		for _, s := range entry.Steps {
			if len(s.Name) > nameWidth {
				nameWidth = len(s.Name)
			}
			for _, sub := range s.SubSteps {
				if needed := len(sub.Name) + subExtraIndent; needed > nameWidth {
					nameWidth = needed
				}
			}
		}

		// Column header
		fmt.Fprintf(w, "  %-*s  %8s\n", nameWidth, "STEP", "DURATION")

		// Step rows
		for _, s := range entry.Steps {
			dur := fmt.Sprintf("%dms", s.DurationMs)
			line := fmt.Sprintf("  %-*s  %8s", nameWidth, s.Name, dur)
			if s.Error {
				line += "  x"
			}
			fmt.Fprintln(w, line)

			// Sub-step rows with ASCII tree connectors.
			// Pad manually to avoid multi-byte UTF-8 box-drawing chars
			// (├─, └─) breaking Go's byte-based %-*s alignment.
			for i, sub := range s.SubSteps {
				connector := "├─"
				if i == len(s.SubSteps)-1 {
					connector = "└─"
				}
				subDur := fmt.Sprintf("%dms", sub.DurationMs)
				pad := nameWidth - subExtraIndent - len(sub.Name)
				if pad < 0 {
					pad = 0
				}
				subLine := fmt.Sprintf("    %s %s%s  %8s", connector, sub.Name, strings.Repeat(" ", pad), subDur)
				if sub.Error {
					subLine += "  x"
				}
				fmt.Fprintln(w, subLine)
			}
		}
	}
}
