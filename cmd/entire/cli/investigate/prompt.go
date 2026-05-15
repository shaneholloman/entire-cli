package investigate

import (
	"fmt"
	"strings"
)

// Files holds the absolute paths to the documents shared across an
// investigation run.
type Files struct {
	// Findings is the absolute path to the findings document the agent
	// reads, edits, and adds evidence to.
	Findings string
	// State is the absolute path to the run's state.json file. The agent
	// records its stance there via the `pending_turn` field.
	State string
}

// ComposeInput is the per-turn data needed to render an investigate prompt.
//
// The struct is intentionally kept narrow: the loop driver passes only what
// the prompt template uses. Marvin's prompt also surfaces prior decisions,
// claims, and fixes from its memory store; entire does not have an
// equivalent surface yet, so callers may pass arbitrary text via
// PriorContext (e.g. checkpoint search excerpts) for rendering.
type ComposeInput struct {
	// Topic is the human-readable subject of the investigation. Used in
	// the body of the prompt as plain text — never as a section heading,
	// since the rendered findings doc owns that.
	Topic string

	// AgentName is the agent the prompt is being rendered for (e.g.
	// "claude-code").
	AgentName string

	// Round is the 1-indexed round number in the loop.
	Round int

	// Turn is the 1-indexed overall turn number across rounds.
	Turn int

	// AlwaysPrompt, if non-empty, is appended verbatim at the end of the
	// rendered prompt. Mirrors ReviewConfig.Prompt so users can inject
	// project-specific guardrails into every turn via settings.
	AlwaysPrompt string

	// Files holds the findings + state absolute paths the agent must
	// read and edit.
	Files Files

	// PriorContext, if non-empty, is rendered as a "## Prior context"
	// block ahead of the main task instructions. Useful for surfacing
	// checkpoint excerpts, search hits, or other historical context that
	// is run-specific rather than baked into the prompt template.
	PriorContext string
}

// ComposeInvestigatePrompt renders the full prompt sent to one agent for one
// turn of an investigate run.
//
// The agent reads Files.Findings, edits it in place, then records its
// stance by writing the `pending_turn` field of Files.State to a JSON
// object of the form
// {"stance":"approve|request-changes|abstain","note":"<one-line>"}.
// The agent must not modify any other field of state.json.
func ComposeInvestigatePrompt(in ComposeInput) string {
	var b strings.Builder

	if pc := strings.TrimSpace(in.PriorContext); pc != "" {
		b.WriteString("## Prior context\n\n")
		b.WriteString(pc)
		b.WriteString("\n\n")
	}

	fmt.Fprintf(&b, `You are participating in an autonomous multi-agent investigation. The agents take turns appending findings, evidence, and analysis to a shared findings document until quorum is reached.

You are agent: %s
Round: %d    (turn %d overall in this session)
Topic: %s

Files:
  Findings: %s
  State:    %s

## Your task this turn

1. Read the findings doc in full before editing — prior agents may have
   already established findings, evidence, or pushback you must engage with
   rather than restate.

2. Form an independent opinion. Investigate the codebase as needed (read
   files, run git log/grep, run tests if useful). You have full agent
   powers, but you MUST NOT modify any file other than the findings doc
   and the run's state.json file (see step 4).

   You are a skeptical investigator: every claim in the findings doc must
   be supported by concrete evidence (file:line refs, command output, or
   test results). Push back on prior agents' claims that lack evidence,
   mark them disputed, or note unknowns. Aim to converge on a complete,
   defensible explanation — not just to add more text.

3. Edit the findings doc to add or refine findings. One numbered subsection
   per finding, with concrete evidence. Keep the TLDR section accurate
   every turn — it should reflect the current best answer, not the
   original question. Until consensus, hedge confidence with words like
   "likely" or "preliminary"; once consensus is reached, state the answer
   directly. Do NOT add a "## Recommendations" or "## Action items"
   section — investigations end at the Conclusion.

4. Report your stance by setting the `+"`pending_turn`"+` field in state.json at:

     %s

   Read the file, set ONLY the `+"`pending_turn`"+` key to a JSON object of
   the form

     {"stance": "approve" | "request-changes" | "abstain", "note": "<one-line explanation>"}

   then write the file back. Do NOT modify any other field of state.json
   — the loop owns everything else.

5. Stance rules:
   - "approve" only if you have independently verified the findings and
     confirm the investigation is complete and correct.
   - "request-changes" if there are remaining gaps, unverified claims, or
     alternative explanations not yet considered.
   - "abstain" if you cannot form an opinion (e.g. insufficient context,
     out-of-scope expertise) — explain why so the next agent can address
     the gap.

6. Do NOT commit anything to git. Do NOT run destructive commands.

7. Exit once you've written your `+"`pending_turn`"+` to state.json.
`,
		in.AgentName,
		in.Round, in.Turn,
		in.Topic,
		in.Files.Findings,
		in.Files.State,
		in.Files.State,
	)

	if ap := strings.TrimSpace(in.AlwaysPrompt); ap != "" {
		b.WriteString("\n")
		b.WriteString(ap)
		b.WriteString("\n")
	}

	return b.String()
}
