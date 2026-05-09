package investigate

import (
	"slices"
	"strings"
	"testing"
)

// TestEnvNamesAreStable pins each ENTIRE_INVESTIGATE_* constant by direct
// comparison so a rename surfaces on the specific constant that broke,
// rather than as one ambiguous map-iteration failure.
func TestEnvNamesAreStable(t *testing.T) {
	t.Parallel()
	if EnvSession != "ENTIRE_INVESTIGATE_SESSION" {
		t.Errorf("EnvSession: got %q, want ENTIRE_INVESTIGATE_SESSION", EnvSession)
	}
	if EnvAgent != "ENTIRE_INVESTIGATE_AGENT" {
		t.Errorf("EnvAgent: got %q, want ENTIRE_INVESTIGATE_AGENT", EnvAgent)
	}
	if EnvRunID != "ENTIRE_INVESTIGATE_RUN_ID" {
		t.Errorf("EnvRunID: got %q, want ENTIRE_INVESTIGATE_RUN_ID", EnvRunID)
	}
	if EnvRound != "ENTIRE_INVESTIGATE_ROUND" {
		t.Errorf("EnvRound: got %q, want ENTIRE_INVESTIGATE_ROUND", EnvRound)
	}
	if EnvTurn != "ENTIRE_INVESTIGATE_TURN" {
		t.Errorf("EnvTurn: got %q, want ENTIRE_INVESTIGATE_TURN", EnvTurn)
	}
	if EnvTopic != "ENTIRE_INVESTIGATE_TOPIC" {
		t.Errorf("EnvTopic: got %q, want ENTIRE_INVESTIGATE_TOPIC", EnvTopic)
	}
	if EnvPrompt != "ENTIRE_INVESTIGATE_PROMPT" {
		t.Errorf("EnvPrompt: got %q, want ENTIRE_INVESTIGATE_PROMPT", EnvPrompt)
	}
	if EnvFindingsDoc != "ENTIRE_INVESTIGATE_FINDINGS_DOC" {
		t.Errorf("EnvFindingsDoc: got %q, want ENTIRE_INVESTIGATE_FINDINGS_DOC", EnvFindingsDoc)
	}
	if EnvTimelineDoc != "ENTIRE_INVESTIGATE_TIMELINE_DOC" {
		t.Errorf("EnvTimelineDoc: got %q, want ENTIRE_INVESTIGATE_TIMELINE_DOC", EnvTimelineDoc)
	}
	if EnvStartingSHA != "ENTIRE_INVESTIGATE_STARTING_SHA" {
		t.Errorf("EnvStartingSHA: got %q, want ENTIRE_INVESTIGATE_STARTING_SHA", EnvStartingSHA)
	}
}

// TestIsInvestigateEnvEntry pins the prefix-matching helper used to strip
// stale ENTIRE_INVESTIGATE_* entries before AppendInvestigateEnv writes new
// ones.
func TestIsInvestigateEnvEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kv   string
		want bool
	}{
		{EnvSession + "=1", true},
		{EnvAgent + "=claude-code", true},
		{EnvRunID + "=abcdef012345", true},
		{EnvRound + "=2", true},
		{EnvTurn + "=5", true},
		{EnvTopic + "=topic", true},
		{EnvPrompt + "=prompt", true},
		{EnvFindingsDoc + "=/tmp/x", true},
		{EnvTimelineDoc + "=/tmp/y", true},
		{EnvStartingSHA + "=deadbeef", true},
		{"PATH=/usr/bin", false},
		{"HOME=/home/u", false},
		{"ENTIRE_REVIEW_SESSION=1", false},    // review entries are not investigate entries
		{"ENTIRE_INVESTIGATE_OTHER=1", false}, // unknown investigate-style key
		{"NOT_ENTIRE_INVESTIGATE_SESSION", false},
	}
	for _, tc := range tests {
		if got := IsInvestigateEnvEntry(tc.kv); got != tc.want {
			t.Errorf("IsInvestigateEnvEntry(%q) = %v, want %v", tc.kv, got, tc.want)
		}
	}
}

// TestAppendInvestigateEnv_StripsStaleInvestigateAndReview pins the contract
// that AppendInvestigateEnv removes both ENTIRE_INVESTIGATE_* and
// ENTIRE_REVIEW_* entries before appending fresh values. The review-strip
// is the risk-mitigation guard for a child investigate process inheriting
// review env from a parent shell.
func TestAppendInvestigateEnv_StripsStaleInvestigateAndReview(t *testing.T) {
	t.Parallel()
	base := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		// stale investigate vars from a previous run
		EnvSession + "=stale",
		EnvAgent + "=stale-agent",
		EnvRunID + "=staleeeeeeee",
		EnvRound + "=99",
		EnvTurn + "=99",
		EnvTopic + "=stale topic",
		EnvPrompt + "=stale prompt",
		EnvFindingsDoc + "=/tmp/stale-findings.md",
		EnvTimelineDoc + "=/tmp/stale-timeline.md",
		EnvStartingSHA + "=stalehash",
		// stale review vars from an outer review process
		"ENTIRE_REVIEW_SESSION=1",
		"ENTIRE_REVIEW_AGENT=stale-review-agent",
		"ENTIRE_REVIEW_SKILLS=[\"/stale\"]",
		"ENTIRE_REVIEW_PROMPT=stale review prompt",
		"ENTIRE_REVIEW_STARTING_SHA=stalehash",
	}
	got := AppendInvestigateEnv(base, AppendOptions{
		AgentName:   "claude-code",
		RunID:       "abcdef012345",
		Round:       1,
		Turn:        2,
		Topic:       "fresh topic",
		Prompt:      "fresh prompt",
		FindingsDoc: "/tmp/fresh-findings.md",
		TimelineDoc: "/tmp/fresh-timeline.md",
		StartingSHA: "freshhash",
	})

	want := map[string]string{
		EnvSession:     "1",
		EnvAgent:       "claude-code",
		EnvRunID:       "abcdef012345",
		EnvRound:       "1",
		EnvTurn:        "2",
		EnvTopic:       "fresh topic",
		EnvPrompt:      "fresh prompt",
		EnvFindingsDoc: "/tmp/fresh-findings.md",
		EnvTimelineDoc: "/tmp/fresh-timeline.md",
		EnvStartingSHA: "freshhash",
	}
	counts := make(map[string]int)
	values := make(map[string]string)
	for _, kv := range got {
		for key := range want {
			prefix := key + "="
			if strings.HasPrefix(kv, prefix) {
				counts[key]++
				values[key] = kv[len(prefix):]
			}
		}
	}
	for key, wantVal := range want {
		if counts[key] != 1 {
			t.Errorf("%s: expected exactly 1 occurrence, got %d", key, counts[key])
		}
		if values[key] != wantVal {
			t.Errorf("%s: got %q, want %q", key, values[key], wantVal)
		}
	}

	// Review entries from the parent must NOT survive — the contract is that
	// they are stripped to prevent cross-tagging.
	for _, kv := range got {
		for _, name := range []string{
			"ENTIRE_REVIEW_SESSION=",
			"ENTIRE_REVIEW_AGENT=",
			"ENTIRE_REVIEW_SKILLS=",
			"ENTIRE_REVIEW_PROMPT=",
			"ENTIRE_REVIEW_STARTING_SHA=",
		} {
			if strings.HasPrefix(kv, name) {
				t.Errorf("review env entry survived strip: %q", kv)
			}
		}
	}

	// Non-investigate, non-review entries must survive unchanged.
	pathSeen := false
	homeSeen := false
	for _, kv := range got {
		if kv == "PATH=/usr/bin" {
			pathSeen = true
		}
		if kv == "HOME=/home/u" {
			homeSeen = true
		}
	}
	if !pathSeen || !homeSeen {
		t.Errorf("unrelated env entries should survive: PATH=%v HOME=%v", pathSeen, homeSeen)
	}
}

// TestAppendInvestigateEnv_AppendsAllKeys checks that even when base has no
// stale entries, every contract key is appended to the returned slice with
// the value from AppendOptions.
func TestAppendInvestigateEnv_AppendsAllKeys(t *testing.T) {
	t.Parallel()
	got := AppendInvestigateEnv(nil, AppendOptions{
		AgentName:   "codex",
		RunID:       "0123456789ab",
		Round:       3,
		Turn:        7,
		Topic:       "topic",
		Prompt:      "prompt",
		FindingsDoc: "/abs/findings.md",
		TimelineDoc: "/abs/timeline.md",
		StartingSHA: "abc123",
	})
	want := []string{
		EnvSession + "=1",
		EnvAgent + "=codex",
		EnvRunID + "=0123456789ab",
		EnvRound + "=3",
		EnvTurn + "=7",
		EnvTopic + "=topic",
		EnvPrompt + "=prompt",
		EnvFindingsDoc + "=/abs/findings.md",
		EnvTimelineDoc + "=/abs/timeline.md",
		EnvStartingSHA + "=abc123",
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing env entry %q in %v", w, got)
		}
	}
}
