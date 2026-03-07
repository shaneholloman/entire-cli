package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// perfStep represents a single timed step within a perf span.
// Group steps (from nested spans) have SubSteps with 0-based iteration numbering.
type perfStep struct {
	Name       string
	DurationMs int64
	Error      bool
	SubSteps   []perfStep
}

// perfEntry represents a parsed performance trace log entry.
type perfEntry struct {
	Op         string
	DurationMs int64
	Error      bool
	Time       time.Time
	Steps      []perfStep
}

// parsePerfEntry parses a JSON log line into a perfEntry.
// Returns nil if the line is not valid JSON or is not a perf entry (msg != "perf").
func parsePerfEntry(line string) *perfEntry {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil
	}

	// Check that msg == "perf"
	var msg string
	if msgRaw, ok := raw["msg"]; !ok {
		return nil
	} else if err := json.Unmarshal(msgRaw, &msg); err != nil || msg != "perf" {
		return nil
	}

	entry := &perfEntry{}

	// Extract op
	if opRaw, ok := raw["op"]; ok {
		if err := json.Unmarshal(opRaw, &entry.Op); err != nil {
			return nil
		}
	}

	// Extract duration_ms
	if dRaw, ok := raw["duration_ms"]; ok {
		if err := json.Unmarshal(dRaw, &entry.DurationMs); err != nil {
			return nil
		}
	}

	// Extract error flag
	if errRaw, ok := raw["error"]; ok {
		if err := json.Unmarshal(errRaw, &entry.Error); err != nil {
			return nil
		}
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
	steps := make([]perfStep, 0, len(parentStepDurations))
	for name, ms := range parentStepDurations {
		step := perfStep{
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
			sort.Ints(indices)

			subList := make([]perfStep, 0, len(subs))
			for _, idx := range indices {
				subList = append(subList, perfStep{
					Name:       fmt.Sprintf("%s.%d", name, idx),
					DurationMs: subs[idx],
					Error:      subStepErrors[name][idx],
				})
			}
			step.SubSteps = subList
		}

		steps = append(steps, step)
	}
	sort.Slice(steps, func(i, j int) bool {
		return steps[i].Name < steps[j].Name
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

// collectPerfEntries reads a JSONL log file and returns the last N perf entries,
// ordered newest first. If hookFilter is non-empty, only entries with a matching
// Op field are included.
func collectPerfEntries(logFile string, last int, hookFilter string) ([]perfEntry, error) {
	f, err := os.Open(logFile) //nolint:gosec // logFile is a CLI-resolved path, not user-supplied input
	if err != nil {
		return nil, fmt.Errorf("opening perf log: %w", err)
	}
	defer f.Close()

	var entries []perfEntry

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		entry := parsePerfEntry(scanner.Text())
		if entry == nil {
			continue
		}
		if hookFilter != "" && entry.Op != hookFilter {
			continue
		}
		entries = append(entries, *entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading perf log: %w", err)
	}

	// Take the last N entries
	if len(entries) > last {
		entries = entries[len(entries)-last:]
	}

	// Reverse so newest entries are first
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	return entries, nil
}

// renderPerfEntries writes a formatted table of perf entries to w.
// If entries is empty, it prints a help message about enabling perf traces.
func renderPerfEntries(w io.Writer, entries []perfEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "No perf entries found.")
		fmt.Fprintln(w, `Perf traces are logged at DEBUG level. Make sure ENTIRE_LOG_LEVEL=DEBUG is set`)
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
