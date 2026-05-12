package investigate

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newTestModel returns a fresh model with three agents and a no-op cancel so
// transition tests do not need to spawn a Bubble Tea program.
func newTestModel(t *testing.T) investigateTUIModel {
	t.Helper()
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newInvestigateTUIModel("a topic", "abcd1234efff",
		[]string{"claude-code", "codex", "gemini-cli"}, 3, 2, cancel)
}

func TestInvestigateTUIModel_TurnLifecycle(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)

	// All rows start queued.
	for i, r := range m.rows {
		if r.status != rowStatusQueued {
			t.Errorf("rows[%d].status = %v, want queued", i, r.status)
		}
	}

	// Start a turn for claude-code → running.
	updated, _ := m.Update(turnStartedMsg{agent: "claude-code", turn: 1})
	m2, ok := updated.(investigateTUIModel)
	if !ok {
		t.Fatalf("Update returned wrong type: %T", updated)
	}
	if got := m2.rows[0].status; got != rowStatusRunning {
		t.Errorf("after TurnStarted: rows[0].status = %v, want running", got)
	}
	if m2.rows[0].currentStart.IsZero() {
		t.Errorf("after TurnStarted: rows[0].currentStart not stamped")
	}

	// Finish that turn with approve → queued, turn count incremented, stance recorded.
	updated2, _ := m2.Update(turnFinishedMsg{
		agent:    "claude-code",
		turn:     1,
		stance:   stanceApprove,
		duration: 5 * time.Second,
		failed:   false,
	})
	m3, ok := updated2.(investigateTUIModel)
	if !ok {
		t.Fatalf("Update returned wrong type: %T", updated2)
	}
	if got := m3.rows[0].status; got != rowStatusQueued {
		t.Errorf("after TurnFinished: rows[0].status = %v, want queued", got)
	}
	if got := m3.rows[0].turnsTaken; got != 1 {
		t.Errorf("after TurnFinished: rows[0].turnsTaken = %d, want 1", got)
	}
	if got := m3.rows[0].latestStance; got != stanceApprove {
		t.Errorf("after TurnFinished: rows[0].latestStance = %q, want approve", got)
	}
	if got := m3.rows[0].accumulated; got != 5*time.Second {
		t.Errorf("after TurnFinished: rows[0].accumulated = %v, want 5s", got)
	}
	if got := m3.approvals; got != 1 {
		t.Errorf("after TurnFinished: m.approvals = %d, want 1", got)
	}
}

func TestInvestigateTUIModel_FailedTurnExhaustsBudget(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	// Three failures in a row for the same agent should flip status to failed
	// because maxTurns is 3.
	for i := 1; i <= 3; i++ {
		updated, _ := m.Update(turnFinishedMsg{
			agent:    "codex",
			turn:     i,
			stance:   stanceUnknown,
			duration: time.Second,
			failed:   true,
		})
		next, ok := updated.(investigateTUIModel)
		if !ok {
			t.Fatalf("Update returned wrong type: %T", updated)
		}
		m = next
	}
	if got := m.rows[1].status; got != rowStatusFailed {
		t.Errorf("after 3 failures: rows[1].status = %v, want failed", got)
	}
}

func TestInvestigateTUIModel_RunFinishedMarksDone(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	// One successful turn for the first agent.
	updated, _ := m.Update(turnFinishedMsg{
		agent:    "claude-code",
		turn:     1,
		stance:   stanceApprove,
		duration: time.Second,
	})
	next, ok := updated.(investigateTUIModel)
	if !ok {
		t.Fatalf("Update returned wrong type after TurnFinished: %T", updated)
	}
	m = next

	updated, _ = m.Update(runFinishedMsg{outcome: OutcomeQuorum})
	next, ok = updated.(investigateTUIModel)
	if !ok {
		t.Fatalf("Update returned wrong type after RunFinished: %T", updated)
	}
	m = next

	if !m.finished {
		t.Errorf("after RunFinished: m.finished = false, want true")
	}
	if got := m.outcome; got != OutcomeQuorum {
		t.Errorf("after RunFinished: m.outcome = %v, want quorum", got)
	}
	for i, r := range m.rows {
		if r.status != rowStatusDone {
			t.Errorf("rows[%d].status = %v, want done", i, r.status)
		}
	}
}

func TestInvestigateTUIModel_View_ColumnHeaders(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	view := m.dashboardView()
	for _, h := range []string{"AGENT", "STATUS", "DURATION", "TURN", "APPROVED"} {
		if !strings.Contains(view, h) {
			t.Errorf("dashboardView missing header %q\nfull view:\n%s", h, view)
		}
	}
	if !strings.Contains(view, "Ctrl+C: cancel") {
		t.Errorf("dashboardView missing cancel hint\nfull view:\n%s", view)
	}
}

func TestFormatStance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{stanceApprove, "✓ approve"},
		{stanceRequestChanges, "✗ changes"},
		{stanceAbstain, "— abstain"},
		{stanceUnknown, ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := formatStance(c.in); got != c.want {
			t.Errorf("formatStance(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   time.Duration
		want string
	}{
		{300 * time.Millisecond, "300ms"},
		{5*time.Second + 200*time.Millisecond, "5.2s"},
		{90 * time.Second, "1m30s"},
	}
	for _, c := range cases {
		if got := formatDuration(c.in); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
