// Package investigate — see env.go for package-level rationale.
//
// tui_model.go provides investigateTUIModel, the Bubble Tea Model for the
// investigate dashboard. The model renders a per-agent status table during
// the run with AGENT / STATUS / DURATION / TURN / APPROVED columns; it
// reacts to ProgressSink events translated into tea.Msg values by
// tui_sink.go.
//
// Mirrors the structure of review/tui_model.go but uses turn-based events
// instead of streaming agent events.
package investigate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// rowStatus is the per-agent terminal state shown in the STATUS column.
type rowStatus int

const (
	rowStatusQueued rowStatus = iota
	rowStatusRunning
	rowStatusDone
	rowStatusFailed
)

// agentRow holds per-agent live state during the TUI run.
type agentRow struct {
	name         string
	status       rowStatus
	currentStart time.Time     // stamped on TurnStarted, zeroed on TurnFinished
	accumulated  time.Duration // sum of completed turn durations
	turnsTaken   int           // increments on TurnFinished (success or fail)
	maxTurns     int
	latestStance string // canonical: "approve" | "request-changes" | "abstain" | ""
	lastErr      error
}

// turnStartedMsg is sent when the loop begins an agent turn.
type turnStartedMsg struct {
	agent string
	turn  int
}

// turnFinishedMsg is sent when the loop finishes an agent turn (success or
// failure).
type turnFinishedMsg struct {
	agent    string
	turn     int
	stance   string
	duration time.Duration
	failed   bool
	err      error
}

// runFinishedMsg is sent once when the loop terminates.
type runFinishedMsg struct {
	outcome LoopOutcome
}

// tickMsg drives spinner + running-duration refresh between turn events.
type tickMsg time.Time

// investigateTUIModel is the Bubble Tea model for the investigate dashboard.
type investigateTUIModel struct {
	topic           string
	runID           string
	rows            []agentRow
	rowIdx          map[string]int
	quorum          int
	approvals       int
	completedRounds int
	maxRounds       int

	finished bool
	outcome  LoopOutcome

	cancel     context.CancelFunc
	cancelOnce *sync.Once

	spinner    spinner.Model
	termWidth  int
	termHeight int
}

// newInvestigateTUIModel builds an initial model pre-populated with one row
// per agent. cancel is invoked when the user presses Ctrl+C inside the TUI.
func newInvestigateTUIModel(topic, runID string, agents []string, maxTurns, quorum int, cancel context.CancelFunc) investigateTUIModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	rows := make([]agentRow, len(agents))
	rowIdx := make(map[string]int, len(agents))
	for i, name := range agents {
		rows[i] = agentRow{
			name:     name,
			status:   rowStatusQueued,
			maxTurns: maxTurns,
		}
		rowIdx[name] = i
	}
	return investigateTUIModel{
		topic:      topic,
		runID:      runID,
		rows:       rows,
		rowIdx:     rowIdx,
		quorum:     quorum,
		maxRounds:  maxTurns,
		cancel:     cancel,
		cancelOnce: &sync.Once{},
		spinner:    sp,
		termWidth:  80,
		termHeight: 24,
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Init kicks off the spinner and the refresh tick.
func (m investigateTUIModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tickCmd())
}

// Update handles all incoming messages.
func (m investigateTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case turnStartedMsg:
		return m.handleTurnStarted(msg), nil

	case turnFinishedMsg:
		return m.handleTurnFinished(msg), nil

	case runFinishedMsg:
		m.finished = true
		m.outcome = msg.outcome
		for i := range m.rows {
			if m.rows[i].status != rowStatusFailed {
				m.rows[i].status = rowStatusDone
			}
			if !m.rows[i].currentStart.IsZero() {
				m.rows[i].accumulated += time.Since(m.rows[i].currentStart)
				m.rows[i].currentStart = time.Time{}
			}
		}
		return m, nil

	case tickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		return m, tea.Batch(spinCmd, tickCmd())

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		return m, spinCmd

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleTurnStarted marks the named agent as running and stamps the start
// time. Unknown agents are ignored (defensive — should not happen given the
// rowIdx pre-population, but we'd rather drop a message than panic).
func (m investigateTUIModel) handleTurnStarted(msg turnStartedMsg) investigateTUIModel {
	idx, ok := m.rowIdx[msg.agent]
	if !ok {
		return m
	}
	row := &m.rows[idx]
	row.status = rowStatusRunning
	row.currentStart = time.Now()
	return m
}

// handleTurnFinished folds the just-completed turn into the row's
// accumulated state and updates the round counters.
func (m investigateTUIModel) handleTurnFinished(msg turnFinishedMsg) investigateTUIModel {
	idx, ok := m.rowIdx[msg.agent]
	if !ok {
		return m
	}
	row := &m.rows[idx]

	row.accumulated += msg.duration
	row.currentStart = time.Time{}
	row.turnsTaken++
	if msg.stance != "" && msg.stance != stanceUnknown {
		row.latestStance = msg.stance
	}
	if msg.failed {
		row.lastErr = msg.err
		if row.turnsTaken >= row.maxTurns {
			row.status = rowStatusFailed
		} else {
			row.status = rowStatusQueued
		}
	} else {
		row.status = rowStatusQueued
	}

	// Recompute round + approval counters from the full row set so we are
	// resilient to out-of-order messages and replays.
	totalTurns := 0
	approvals := 0
	for _, r := range m.rows {
		totalTurns += r.turnsTaken
		if r.latestStance == stanceApprove {
			approvals++
		}
	}
	if n := len(m.rows); n > 0 {
		m.completedRounds = totalTurns / n
	}
	m.approvals = approvals
	return m
}

// handleKey processes keyboard input.
func (m investigateTUIModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.finished {
		// Any key after finished dismisses.
		return m, tea.Quit
	}
	if msg.Code == 'c' && msg.Mod == tea.ModCtrl {
		m.cancelOnce.Do(m.cancel)
		return m, tea.Quit
	}
	return m, nil
}

// View renders the current frame.
func (m investigateTUIModel) View() tea.View {
	return tea.NewView(m.dashboardView())
}

// dashboardWidth returns the effective rendering width (defaulted when the
// terminal hasn't reported a size yet).
func (m investigateTUIModel) dashboardWidth() int {
	if m.termWidth <= 0 {
		return 80
	}
	return m.termWidth
}

// dashboardView renders the top banner, the table, and the footer hint.
func (m investigateTUIModel) dashboardView() string {
	var b strings.Builder

	m.writeLine(&b, m.titleLine())
	m.writeLine(&b, m.progressLine())
	b.WriteString("\n")
	m.writeLine(&b, m.headerLine())
	for _, row := range m.rows {
		m.writeLine(&b, m.renderRow(row))
	}
	b.WriteString("\n")
	if m.finished {
		m.writeLine(&b, m.outcomeLine())
		m.writeLine(&b, m.countsLine())
		m.writeLine(&b, "Press any key to exit.")
	} else {
		m.writeLine(&b, "Ctrl+C: cancel")
	}
	return b.String()
}

func (m investigateTUIModel) writeLine(b *strings.Builder, line string) {
	b.WriteString(truncateDisplayWidth(line, m.dashboardWidth()))
	b.WriteString("\n")
}

func (m investigateTUIModel) titleLine() string {
	if m.topic == "" {
		return fmt.Sprintf("Investigating (run %s)", m.runID)
	}
	return fmt.Sprintf("Investigating: %q (run %s)", sanitizeDisplayText(m.topic), m.runID)
}

func (m investigateTUIModel) progressLine() string {
	totalTurns := 0
	for _, r := range m.rows {
		totalTurns += r.turnsTaken
	}
	maxOverall := m.maxRounds * len(m.rows)
	round := m.completedRounds + 1
	if m.finished {
		round = max(m.completedRounds, 1)
	}
	return fmt.Sprintf("Round %d/%d · %d of %d turns · quorum %d/%d approvals",
		round, m.maxRounds, totalTurns, maxOverall, m.approvals, m.quorum)
}

func (m investigateTUIModel) headerLine() string {
	return m.renderTableLine("AGENT", "STATUS", "DURATION", "TURN", "APPROVED")
}

func (m investigateTUIModel) renderRow(row agentRow) string {
	statusStr := m.statusString(row)
	durStr := formatRowDuration(row)
	turnStr := fmt.Sprintf("%d/%d", row.turnsTaken, row.maxTurns)
	approvedStr := formatStance(row.latestStance)
	return m.renderTableLine(row.name, statusStr, durStr, turnStr, approvedStr)
}

// statusString renders STATUS for a row, including the live dot spinner for
// the currently-running agent.
func (m investigateTUIModel) statusString(row agentRow) string {
	switch row.status {
	case rowStatusRunning:
		return m.spinner.View() + " running"
	case rowStatusDone:
		return "✓ done"
	case rowStatusFailed:
		return "✗ failed"
	case rowStatusQueued:
		fallthrough
	default:
		return "queued"
	}
}

// renderTableLine emits one row of the table padded to fixed column widths.
// APPROVED takes whatever's left after the four fixed columns.
func (m investigateTUIModel) renderTableLine(agent, status, duration, turn, approved string) string {
	const (
		agentWidth    = 20
		statusWidth   = 12
		durationWidth = 9
		turnWidth     = 6
		separators    = 8 // four two-space separators between five columns
		minWidth      = agentWidth + statusWidth + durationWidth + turnWidth + separators
	)
	termWidth := m.dashboardWidth()
	approvedWidth := max(termWidth-minWidth, 0)
	line := fmt.Sprintf("%s  %s  %s  %s  %s",
		padDisplayWidth(agent, agentWidth),
		padDisplayWidth(status, statusWidth),
		padDisplayWidth(duration, durationWidth),
		padDisplayWidth(turn, turnWidth),
		truncateDisplayWidth(approved, approvedWidth))
	return truncateDisplayWidth(line, termWidth)
}

// outcomeLine renders the post-run "Outcome: <name>" summary.
func (m investigateTUIModel) outcomeLine() string {
	if m.outcome == "" {
		return ""
	}
	return fmt.Sprintf("Outcome: %s", m.outcome)
}

// countsLine renders the per-stance totals at the end of the run.
func (m investigateTUIModel) countsLine() string {
	app, chg, abs, unk := 0, 0, 0, 0
	for _, r := range m.rows {
		switch r.latestStance {
		case stanceApprove:
			app++
		case stanceRequestChanges:
			chg++
		case stanceAbstain:
			abs++
		default:
			unk++
		}
	}
	return fmt.Sprintf("%d agent(s) — %d approved, %d request-changes, %d abstain, %d unknown",
		len(m.rows), app, chg, abs, unk)
}

// formatRowDuration returns the display string for the DURATION column.
// While running it accumulates the in-flight elapsed time; otherwise it
// shows the total accumulated across completed turns. Empty when nothing
// has run yet.
func formatRowDuration(row agentRow) string {
	total := row.accumulated
	if !row.currentStart.IsZero() {
		total += time.Since(row.currentStart)
	}
	if total <= 0 {
		return ""
	}
	return formatDuration(total)
}

// formatDuration is a near-copy of review.formatDuration. Kept private so
// the investigate TUI does not pull in the review package.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// formatStance renders the APPROVED column from a canonical stance.
func formatStance(stance string) string {
	switch stance {
	case stanceApprove:
		return "✓ approve"
	case stanceRequestChanges:
		return "✗ changes"
	case stanceAbstain:
		return "— abstain"
	default:
		return ""
	}
}
