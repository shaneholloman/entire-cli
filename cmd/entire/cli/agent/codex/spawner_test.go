package codex

import (
	"context"
	"io"
	"reflect"
	"testing"
)

// TestCodexSpawner_Name asserts the spawner reports the stable registry name.
func TestCodexSpawner_Name(t *testing.T) {
	t.Parallel()
	if got := NewSpawner().Name(); got != wantCodexAgentName {
		t.Errorf("Name() = %q, want %q", got, wantCodexAgentName)
	}
}

// TestCodexSpawner_Argv pins the argv + stdin contract for the
// non-investigate case (review, generate, etc.):
//
//	codex exec --skip-git-repo-check -s workspace-write -
//
// Prompt is piped on stdin. --sandbox workspace-write is required so the
// agent can write files in the working tree; review reads only so the
// flag is a no-op for that path.
func TestCodexSpawner_Argv(t *testing.T) {
	t.Parallel()
	env := []string{"FOO=bar", "BAZ=qux"}
	cmd := NewSpawner().BuildCmd(context.Background(), env, "the-prompt")

	wantArgs := []string{wantCodexAgentName, "exec", "--skip-git-repo-check", "-s", "workspace-write", "-"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", cmd.Args, wantArgs)
	}

	if !reflect.DeepEqual(cmd.Env, env) {
		t.Errorf("Env = %v, want %v", cmd.Env, env)
	}

	if cmd.Stdin == nil {
		t.Fatal("Stdin = nil, want a reader carrying the prompt")
	}
	got, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	if string(got) != "the-prompt" {
		t.Errorf("stdin = %q, want %q", string(got), "the-prompt")
	}
}

// TestCodexSpawner_Argv_InvestigateAddsAddDir verifies the investigate
// branch: when ENTIRE_INVESTIGATE_FINDINGS_DOC is set in env, the
// spawner appends --add-dir <run-dir> so codex can write findings.md
// and state.json which live under <git-common-dir>/.../<run-id>/.
func TestCodexSpawner_Argv_InvestigateAddsAddDir(t *testing.T) {
	t.Parallel()
	findingsPath := "/repo/.git/entire-investigations/abcdef012345/findings.md"
	env := []string{
		"FOO=bar",
		"ENTIRE_INVESTIGATE_FINDINGS_DOC=" + findingsPath,
	}
	cmd := NewSpawner().BuildCmd(context.Background(), env, "prompt")

	wantArgs := []string{
		wantCodexAgentName, "exec", "--skip-git-repo-check",
		"-s", "workspace-write",
		"--add-dir", "/repo/.git/entire-investigations/abcdef012345",
		"-",
	}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", cmd.Args, wantArgs)
	}
}

// TestCodexSpawner_Argv_InvestigateMissingPathSkipsAddDir verifies that
// an empty FINDINGS_DOC value doesn't produce a stray --add-dir flag.
func TestCodexSpawner_Argv_InvestigateMissingPathSkipsAddDir(t *testing.T) {
	t.Parallel()
	env := []string{"ENTIRE_INVESTIGATE_FINDINGS_DOC="}
	cmd := NewSpawner().BuildCmd(context.Background(), env, "prompt")
	for i, a := range cmd.Args {
		if a == "--add-dir" {
			t.Errorf("unexpected --add-dir in Args[%d]: %v", i, cmd.Args)
		}
	}
}
