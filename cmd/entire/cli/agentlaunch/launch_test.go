package agentlaunch

import (
	"slices"
	"testing"
)

// TestWithoutReviewOrInvestigateEnv pins the contract that the helper
// strips both ENTIRE_REVIEW_* and ENTIRE_INVESTIGATE_* entries from the
// supplied env slice while leaving unrelated entries untouched. This is
// the leak-prevention guarantee for fix-agent launches: a parent shell
// may have inherited stale provenance vars, and the fix session must not
// be tagged as a review or investigate session.
//
// The literal env names below mirror the constants in
// cmd/entire/cli/review/env.go and cmd/entire/cli/investigate/env.go.
// We use literals (not the exported constants) because importing review
// or investigate from this package would create a build cycle: review
// depends on agentlaunch.
func TestWithoutReviewOrInvestigateEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []string
		want     []string
		notWant  []string
		wantSize int
	}{
		{
			name: "strips review and investigate, keeps unrelated",
			input: []string{
				"PATH=/usr/bin",
				"HOME=/home/u",
				"ENTIRE_REVIEW_SESSION=1",
				"ENTIRE_REVIEW_AGENT=claude-code",
				"ENTIRE_REVIEW_SKILLS=[\"/x\"]",
				"ENTIRE_REVIEW_PROMPT=stale review prompt",
				"ENTIRE_REVIEW_STARTING_SHA=stale1",
				"ENTIRE_INVESTIGATE_SESSION=1",
				"ENTIRE_INVESTIGATE_AGENT=claude-code",
				"ENTIRE_INVESTIGATE_RUN_ID=abcdef012345",
				"ENTIRE_INVESTIGATE_ROUND=1",
				"ENTIRE_INVESTIGATE_TURN=2",
				"ENTIRE_INVESTIGATE_TOPIC=topic",
				"ENTIRE_INVESTIGATE_PROMPT=prompt",
				"ENTIRE_INVESTIGATE_FINDINGS_DOC=/tmp/f.md",
				"ENTIRE_INVESTIGATE_TIMELINE_DOC=/tmp/t.md",
				"ENTIRE_INVESTIGATE_STARTING_SHA=stale2",
			},
			want: []string{
				"PATH=/usr/bin",
				"HOME=/home/u",
			},
			notWant: []string{
				"ENTIRE_REVIEW_SESSION=1",
				"ENTIRE_REVIEW_AGENT=claude-code",
				"ENTIRE_REVIEW_SKILLS=[\"/x\"]",
				"ENTIRE_REVIEW_PROMPT=stale review prompt",
				"ENTIRE_REVIEW_STARTING_SHA=stale1",
				"ENTIRE_INVESTIGATE_SESSION=1",
				"ENTIRE_INVESTIGATE_AGENT=claude-code",
				"ENTIRE_INVESTIGATE_RUN_ID=abcdef012345",
				"ENTIRE_INVESTIGATE_ROUND=1",
				"ENTIRE_INVESTIGATE_TURN=2",
				"ENTIRE_INVESTIGATE_TOPIC=topic",
				"ENTIRE_INVESTIGATE_PROMPT=prompt",
				"ENTIRE_INVESTIGATE_FINDINGS_DOC=/tmp/f.md",
				"ENTIRE_INVESTIGATE_TIMELINE_DOC=/tmp/t.md",
				"ENTIRE_INVESTIGATE_STARTING_SHA=stale2",
			},
			wantSize: 2,
		},
		{
			name: "no provenance entries: passthrough",
			input: []string{
				"PATH=/usr/bin",
				"FOO=bar",
			},
			want: []string{
				"PATH=/usr/bin",
				"FOO=bar",
			},
			wantSize: 2,
		},
		{
			name:     "empty input: empty output",
			input:    nil,
			wantSize: 0,
		},
		{
			name: "only provenance entries: empty output",
			input: []string{
				"ENTIRE_REVIEW_SESSION=1",
				"ENTIRE_INVESTIGATE_SESSION=1",
			},
			notWant: []string{
				"ENTIRE_REVIEW_SESSION=1",
				"ENTIRE_INVESTIGATE_SESSION=1",
			},
			wantSize: 0,
		},
		{
			name: "look-alike non-provenance keys survive",
			input: []string{
				"NOT_ENTIRE_REVIEW_SESSION=1",
				"ENTIRE_REVIEW_OTHER=keep",      // not a known prefix
				"ENTIRE_INVESTIGATE_OTHER=keep", // not a known prefix
			},
			want: []string{
				"NOT_ENTIRE_REVIEW_SESSION=1",
				"ENTIRE_REVIEW_OTHER=keep",
				"ENTIRE_INVESTIGATE_OTHER=keep",
			},
			wantSize: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := withoutReviewOrInvestigateEnv(tc.input)
			if len(got) != tc.wantSize {
				t.Errorf("len = %d, want %d (got: %v)", len(got), tc.wantSize, got)
			}
			for _, kv := range tc.want {
				if !slices.Contains(got, kv) {
					t.Errorf("missing expected entry %q in %v", kv, got)
				}
			}
			for _, kv := range tc.notWant {
				if slices.Contains(got, kv) {
					t.Errorf("unexpected entry survived strip: %q", kv)
				}
			}
		})
	}
}

// TestWithoutReviewOrInvestigateEnv_DoesNotMutateInput pins that the
// helper returns a fresh slice and never mutates its argument. Callers
// rely on this when they pass `os.Environ()` directly.
func TestWithoutReviewOrInvestigateEnv_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	input := []string{
		"PATH=/usr/bin",
		"ENTIRE_REVIEW_SESSION=1",
		"ENTIRE_INVESTIGATE_SESSION=1",
		"HOME=/home/u",
	}
	original := slices.Clone(input)

	_ = withoutReviewOrInvestigateEnv(input)

	if !slices.Equal(input, original) {
		t.Errorf("input was mutated: got %v, want %v", input, original)
	}
}
