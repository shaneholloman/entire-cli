package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testOpPostCommit = "post-commit"

func TestParsePerfEntry(t *testing.T) {
	t.Parallel()

	t.Run("valid perf entry", func(t *testing.T) {
		t.Parallel()

		line := `{"time":"2026-01-15T10:30:00.000Z","level":"DEBUG","msg":"perf","component":"perf","op":"post-commit","duration_ms":150,"error":true,"steps.load_session_ms":50,"steps.save_checkpoint_ms":80,"steps.save_checkpoint_err":true}`

		entry := parsePerfEntry(line)
		if entry == nil {
			t.Fatal("parsePerfEntry returned nil for valid perf entry")
		}

		if entry.Op != testOpPostCommit {
			t.Errorf("Op = %q, want %q", entry.Op, testOpPostCommit)
		}
		if entry.DurationMs != 150 {
			t.Errorf("DurationMs = %d, want %d", entry.DurationMs, 150)
		}
		if !entry.Error {
			t.Error("Error = false, want true")
		}

		expectedTime, err := time.Parse(time.RFC3339, "2026-01-15T10:30:00.000Z")
		if err != nil {
			t.Fatalf("failed to parse expected time: %v", err)
		}
		if !entry.Time.Equal(expectedTime) {
			t.Errorf("Time = %v, want %v", entry.Time, expectedTime)
		}

		if len(entry.Steps) != 2 {
			t.Fatalf("len(Steps) = %d, want 2", len(entry.Steps))
		}

		// Steps are sorted alphabetically by name
		if entry.Steps[0].Name != "load_session" {
			t.Errorf("Steps[0].Name = %q, want %q", entry.Steps[0].Name, "load_session")
		}
		if entry.Steps[0].DurationMs != 50 {
			t.Errorf("Steps[0].DurationMs = %d, want %d", entry.Steps[0].DurationMs, 50)
		}
		if entry.Steps[0].Error {
			t.Error("Steps[0].Error = true, want false")
		}

		if entry.Steps[1].Name != "save_checkpoint" {
			t.Errorf("Steps[1].Name = %q, want %q", entry.Steps[1].Name, "save_checkpoint")
		}
		if entry.Steps[1].DurationMs != 80 {
			t.Errorf("Steps[1].DurationMs = %d, want %d", entry.Steps[1].DurationMs, 80)
		}
		if !entry.Steps[1].Error {
			t.Error("Steps[1].Error = false, want true")
		}
	})

	t.Run("non-perf entry returns nil", func(t *testing.T) {
		t.Parallel()

		line := `{"time":"2026-01-15T10:30:00.000Z","level":"INFO","msg":"hook invoked","component":"lifecycle","hook":"post-commit"}`

		entry := parsePerfEntry(line)
		if entry != nil {
			t.Errorf("parsePerfEntry returned %+v for non-perf entry, want nil", entry)
		}
	})

	t.Run("invalid JSON returns nil", func(t *testing.T) {
		t.Parallel()

		entry := parsePerfEntry("this is not json at all{{{")
		if entry != nil {
			t.Errorf("parsePerfEntry returned %+v for invalid JSON, want nil", entry)
		}
	})
}

func TestCollectPerfEntries(t *testing.T) {
	t.Parallel()

	// Fixture: 4 lines — 2 prepare-commit-msg, 1 non-perf, 1 post-commit
	fixtureLines := []string{
		`{"time":"2026-01-15T10:00:00Z","level":"DEBUG","msg":"perf","op":"prepare-commit-msg","duration_ms":100}`,
		`{"time":"2026-01-15T10:01:00Z","level":"DEBUG","msg":"perf","op":"prepare-commit-msg","duration_ms":120}`,
		`{"time":"2026-01-15T10:02:00Z","level":"INFO","msg":"hook invoked","component":"lifecycle","hook":"post-commit"}`,
		`{"time":"2026-01-15T10:03:00Z","level":"DEBUG","msg":"perf","op":"post-commit","duration_ms":200}`,
	}
	fixtureContent := strings.Join(fixtureLines, "\n") + "\n"

	writeFixture := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "perf.jsonl")
		if err := os.WriteFile(p, []byte(fixtureContent), 0o644); err != nil {
			t.Fatalf("failed to write fixture: %v", err)
		}
		return p
	}

	t.Run("last 2 entries", func(t *testing.T) {
		t.Parallel()
		logFile := writeFixture(t)

		entries, err := collectPerfEntries(logFile, 2, "")
		if err != nil {
			t.Fatalf("collectPerfEntries returned error: %v", err)
		}

		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2", len(entries))
		}

		// Newest first: post-commit (line 4), then prepare-commit-msg (line 2)
		if entries[0].Op != testOpPostCommit {
			t.Errorf("entries[0].Op = %q, want %q", entries[0].Op, testOpPostCommit)
		}
		if entries[0].DurationMs != 200 {
			t.Errorf("entries[0].DurationMs = %d, want %d", entries[0].DurationMs, 200)
		}
		if entries[1].Op != "prepare-commit-msg" {
			t.Errorf("entries[1].Op = %q, want %q", entries[1].Op, "prepare-commit-msg")
		}
		if entries[1].DurationMs != 120 {
			t.Errorf("entries[1].DurationMs = %d, want %d", entries[1].DurationMs, 120)
		}
	})

	t.Run("filter by hook type", func(t *testing.T) {
		t.Parallel()
		logFile := writeFixture(t)

		entries, err := collectPerfEntries(logFile, 10, testOpPostCommit)
		if err != nil {
			t.Fatalf("collectPerfEntries returned error: %v", err)
		}

		if len(entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(entries))
		}
		if entries[0].Op != testOpPostCommit {
			t.Errorf("entries[0].Op = %q, want %q", entries[0].Op, testOpPostCommit)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		t.Parallel()

		_, err := collectPerfEntries("/nonexistent/path/perf.jsonl", 10, "")
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}

func TestRenderPerfEntries(t *testing.T) {
	t.Parallel()

	entries := []perfEntry{
		{
			Op:         "post-commit",
			DurationMs: 250,
			Time:       time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
			Steps: []perfStep{
				{Name: "load_session", DurationMs: 50, Error: false},
				{Name: "save_checkpoint", DurationMs: 80, Error: true},
				{Name: "condense", DurationMs: 120, Error: false},
			},
		},
	}

	var buf bytes.Buffer
	renderPerfEntries(&buf, entries)
	out := buf.String()

	// Verify header contains op name, total duration, and timestamp
	if !strings.Contains(out, "post-commit") {
		t.Errorf("output missing op name 'post-commit':\n%s", out)
	}
	if !strings.Contains(out, "250ms") {
		t.Errorf("output missing total duration '250ms':\n%s", out)
	}
	if !strings.Contains(out, "2026-01-15T10:30:00Z") {
		t.Errorf("output missing RFC3339 timestamp:\n%s", out)
	}

	// Verify step names and durations appear
	if !strings.Contains(out, "load_session") {
		t.Errorf("output missing step name 'load_session':\n%s", out)
	}
	if !strings.Contains(out, "50ms") {
		t.Errorf("output missing step duration '50ms':\n%s", out)
	}
	if !strings.Contains(out, "save_checkpoint") {
		t.Errorf("output missing step name 'save_checkpoint':\n%s", out)
	}
	if !strings.Contains(out, "80ms") {
		t.Errorf("output missing step duration '80ms':\n%s", out)
	}
	if !strings.Contains(out, "condense") {
		t.Errorf("output missing step name 'condense':\n%s", out)
	}
	if !strings.Contains(out, "120ms") {
		t.Errorf("output missing step duration '120ms':\n%s", out)
	}

	// Verify column header
	if !strings.Contains(out, "STEP") {
		t.Errorf("output missing column header 'STEP':\n%s", out)
	}
	if !strings.Contains(out, "DURATION") {
		t.Errorf("output missing column header 'DURATION':\n%s", out)
	}

	// Verify error marker on the errored step line
	lines := strings.Split(out, "\n")
	foundErrorMarker := false
	for _, line := range lines {
		if strings.Contains(line, "save_checkpoint") && strings.Contains(line, "x") {
			foundErrorMarker = true
			break
		}
	}
	if !foundErrorMarker {
		t.Errorf("output missing error marker 'x' on save_checkpoint line:\n%s", out)
	}
}

func TestParsePerfEntry_WithSubSteps(t *testing.T) {
	t.Parallel()

	line := `{"time":"2026-01-15T10:30:00Z","level":"DEBUG","msg":"perf","op":"post-commit","duration_ms":350,"steps.process_sessions_ms":300,"steps.process_sessions.0_ms":100,"steps.process_sessions.1_ms":200,"steps.process_sessions.1_err":true,"steps.cleanup_ms":50}`

	entry := parsePerfEntry(line)
	if entry == nil {
		t.Fatal("parsePerfEntry returned nil")
	}

	if len(entry.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(entry.Steps))
	}

	// Steps sorted alphabetically: cleanup, process_sessions
	if entry.Steps[0].Name != "cleanup" {
		t.Errorf("Steps[0].Name = %q, want %q", entry.Steps[0].Name, "cleanup")
	}
	if len(entry.Steps[0].SubSteps) != 0 {
		t.Errorf("Steps[0] should have no sub-steps, got %d", len(entry.Steps[0].SubSteps))
	}

	ps := entry.Steps[1]
	if ps.Name != "process_sessions" {
		t.Errorf("Steps[1].Name = %q, want %q", ps.Name, "process_sessions")
	}
	if ps.DurationMs != 300 {
		t.Errorf("Steps[1].DurationMs = %d, want %d", ps.DurationMs, 300)
	}
	if len(ps.SubSteps) != 2 {
		t.Fatalf("Steps[1] should have 2 sub-steps, got %d", len(ps.SubSteps))
	}
	if ps.SubSteps[0].Name != "process_sessions.0" {
		t.Errorf("SubSteps[0].Name = %q, want %q", ps.SubSteps[0].Name, "process_sessions.0")
	}
	if ps.SubSteps[0].DurationMs != 100 {
		t.Errorf("SubSteps[0].DurationMs = %d, want %d", ps.SubSteps[0].DurationMs, 100)
	}
	if ps.SubSteps[0].Error {
		t.Error("SubSteps[0].Error = true, want false")
	}
	if ps.SubSteps[1].Name != "process_sessions.1" {
		t.Errorf("SubSteps[1].Name = %q, want %q", ps.SubSteps[1].Name, "process_sessions.1")
	}
	if ps.SubSteps[1].DurationMs != 200 {
		t.Errorf("SubSteps[1].DurationMs = %d, want %d", ps.SubSteps[1].DurationMs, 200)
	}
	if !ps.SubSteps[1].Error {
		t.Error("SubSteps[1].Error = false, want true")
	}
}

func TestRenderPerfEntries_WithSubSteps(t *testing.T) {
	t.Parallel()

	entries := []perfEntry{
		{
			Op:         "post-commit",
			DurationMs: 350,
			Steps: []perfStep{
				{Name: "process_sessions", DurationMs: 300, SubSteps: []perfStep{
					{Name: "process_sessions.0", DurationMs: 100},
					{Name: "process_sessions.1", DurationMs: 200, Error: true},
				}},
				{Name: "cleanup", DurationMs: 50},
			},
		},
	}

	var buf bytes.Buffer
	renderPerfEntries(&buf, entries)
	out := buf.String()

	// Parent step appears
	if !strings.Contains(out, "process_sessions") {
		t.Errorf("output missing group step 'process_sessions':\n%s", out)
	}
	if !strings.Contains(out, "300ms") {
		t.Errorf("output missing group duration '300ms':\n%s", out)
	}

	// Sub-steps with tree connectors
	if !strings.Contains(out, "├─ process_sessions.0") {
		t.Errorf("output missing sub-step with tree connector '├─ process_sessions.0':\n%s", out)
	}
	if !strings.Contains(out, "└─ process_sessions.1") {
		t.Errorf("output missing sub-step with tree connector '└─ process_sessions.1':\n%s", out)
	}

	// Error marker on sub-step
	lines := strings.Split(out, "\n")
	foundSubError := false
	for _, line := range lines {
		if strings.Contains(line, "process_sessions.1") && strings.Contains(line, "x") {
			foundSubError = true
			break
		}
	}
	if !foundSubError {
		t.Errorf("output missing error marker on process_sessions.1:\n%s", out)
	}

	// Regular step still appears
	if !strings.Contains(out, "cleanup") {
		t.Errorf("output missing regular step 'cleanup':\n%s", out)
	}
}

func TestPerfCmd_InvalidLastFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"zero", []string{"--last", "0"}, "--last must be at least 1, got 0"},
		{"negative", []string{"--last", "-1"}, "--last must be at least 1, got -1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := newPerfCmd()
			cmd.SetArgs(tt.args)
			cmd.SilenceUsage = true

			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != tt.want {
				t.Errorf("error = %q, want %q", err.Error(), tt.want)
			}
		})
	}
}

func TestRenderPerfEntries_Empty(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	renderPerfEntries(&buf, nil)
	out := buf.String()

	if !strings.Contains(out, "No perf entries found.") {
		t.Errorf("expected 'No perf entries found.' message, got:\n%s", out)
	}
}
