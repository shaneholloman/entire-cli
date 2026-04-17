package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	testUser = "octocat"
	cmdGit   = "git"
)

func TestSlugifyRepoName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"my-project":          "my-project",
		"My Cool Project":     "My-Cool-Project",
		"weird@@@name!!":      "weird-name",
		"":                    "my-repo",
		"---":                 "my-repo",
		"foo__bar":            "foo__bar",
		"a.b.c":               "a.b.c",
		"leading space":       "leading-space",
		"double  space  here": "double-space-here",
	}
	for in, want := range cases {
		if got := slugifyRepoName(in); got != want {
			t.Errorf("slugifyRepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateRepoName(t *testing.T) {
	t.Parallel()
	valid := []string{"my-repo", "foo_bar", "a.b.c", "Repo123", "x"}
	for _, name := range valid {
		if err := validateRepoName(name); err != nil {
			t.Errorf("validateRepoName(%q) unexpectedly returned error: %v", name, err)
		}
	}
	invalid := []string{"", "-leading", ".leading", "has/slash", "has space", strings.Repeat("a", 101)}
	for _, name := range invalid {
		if err := validateRepoName(name); err == nil {
			t.Errorf("validateRepoName(%q) = nil, want error", name)
		}
	}
}

// fakeRunner is a test seam for bootstrapRunner. Each (name, args[0]) pair
// maps to a response.
type fakeRunner struct {
	mu          sync.Mutex
	responses   map[string]fakeResponse
	interactive map[string]error
	calls       []fakeCall
}

type fakeResponse struct {
	stdout string
	err    error
}

type fakeCall struct {
	dir  string
	name string
	args []string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		responses:   make(map[string]fakeResponse),
		interactive: make(map[string]error),
	}
}

func (f *fakeRunner) key(name string, args []string) string {
	return name + " " + strings.Join(args, " ")
}

func (f *fakeRunner) set(name string, args []string, stdout string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[f.key(name, args)] = fakeResponse{stdout: stdout, err: err}
}

func (f *fakeRunner) setInteractive(name string, args []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactive[f.key(name, args)] = err
}

func (f *fakeRunner) lookup(name string, args []string) (fakeResponse, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.responses[f.key(name, args)]
	return r, ok
}

func (f *fakeRunner) record(dir, name string, args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{dir: dir, name: name, args: args})
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.record("", name, args)
	if r, ok := f.lookup(name, args); ok {
		return r.stdout, r.err
	}
	return "", fmt.Errorf("fakeRunner: unexpected call %s %v", name, args)
}

func (f *fakeRunner) RunInDir(_ context.Context, dir, name string, args ...string) (string, error) {
	f.record(dir, name, args)
	if r, ok := f.lookup(name, args); ok {
		return r.stdout, r.err
	}
	return "", fmt.Errorf("fakeRunner: unexpected call in %s: %s %v", dir, name, args)
}

func (f *fakeRunner) RunInteractive(_ context.Context, dir, name string, args ...string) error {
	f.record(dir, name, args)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.interactive[f.key(name, args)]
}

// setIdentityConfigured simulates `git config --get user.name/email` returning
// non-empty values, so ensureGitIdentity treats identity as already set.
func (f *fakeRunner) setIdentityConfigured() {
	f.set("git", []string{"config", "--get", "user.name"}, "Test User\n", nil)
	f.set("git", []string{"config", "--get", "user.email"}, "test@example.com\n", nil)
}

// hasCall returns whether any recorded call matches the predicate.
func (f *fakeRunner) hasCall(match func(fakeCall) bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if match(c) {
			return true
		}
	}
	return false
}

func TestGhHelpers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newFakeRunner()

	r.set("gh", []string{"--version"}, "gh version 2.81.0\n", nil)
	r.set("gh", []string{"auth", "status"}, "Logged in", nil)
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "octocat\n", nil)
	r.set("gh", []string{"api", "user/orgs", "--jq", ".[].login"}, "gamma\nalpha\n\nbeta\n", nil)

	if !ghAvailable(ctx, r) {
		t.Fatal("ghAvailable should be true")
	}
	if !ghAuthenticated(ctx, r) {
		t.Fatal("ghAuthenticated should be true")
	}
	user, err := ghCurrentUser(ctx, r)
	if err != nil || user != testUser {
		t.Fatalf("ghCurrentUser = %q, %v; want octocat", user, err)
	}
	orgs, err := ghListOrgs(ctx, r)
	if err != nil {
		t.Fatalf("ghListOrgs error: %v", err)
	}
	// Must be sorted, trimmed, and blank-skipped.
	want := []string{"alpha", "beta", "gamma"}
	if len(orgs) != len(want) {
		t.Fatalf("orgs = %v, want %v", orgs, want)
	}
	for i, o := range orgs {
		if o != want[i] {
			t.Fatalf("orgs[%d] = %q, want %q", i, o, want[i])
		}
	}
}

func TestGhAvailable_Missing(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("gh", []string{"--version"}, "", errors.New("not found"))
	if ghAvailable(context.Background(), r) {
		t.Fatal("expected ghAvailable to return false when gh is missing")
	}
}

func TestResolveOwner_FlagAcceptsUnknown(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	owner, err := resolveOwner(&buf, testUser, []string{"acme"}, GitHubBootstrapOptions{RepoOwner: "external-org"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "external-org" {
		t.Fatalf("owner = %q, want external-org", owner)
	}
}

func TestResolveOwner_SingleDefault(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	owner, err := resolveOwner(&buf, testUser, nil, GitHubBootstrapOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != testUser {
		t.Fatalf("owner = %q, want octocat", owner)
	}
	if !strings.Contains(buf.String(), testUser) {
		t.Fatalf("expected owner announcement, got %q", buf.String())
	}
}

func TestResolveVisibility_FlagInternalRequiresOrg(t *testing.T) {
	t.Parallel()
	_, err := resolveVisibility(io.Discard, testUser, testUser, GitHubBootstrapOptions{RepoVisibility: "internal"})
	if err == nil {
		t.Fatal("expected error for internal visibility on user repo")
	}
}

func TestResolveVisibility_FlagValid(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"public", "private", "internal"} {
		owner := testUser
		current := testUser
		if v == "internal" {
			owner = "acme"
		}
		got, err := resolveVisibility(io.Discard, owner, current, GitHubBootstrapOptions{RepoVisibility: v})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", v, err)
		}
		if got != v {
			t.Fatalf("%s: got %q", v, got)
		}
	}
}

func TestResolveVisibility_FlagInvalid(t *testing.T) {
	t.Parallel()
	_, err := resolveVisibility(io.Discard, testUser, testUser, GitHubBootstrapOptions{RepoVisibility: "weird"})
	if err == nil {
		t.Fatal("expected error for invalid visibility")
	}
}

func TestResolveRepoName_FlagValidates(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// Return a non-ExitError; ghRepoExists then bubbles up, and resolveRepoName
	// logs a warning but proceeds with the flag-supplied name.
	r.set("gh", []string{"repo", "view", "octocat/ok-name", "--json", "name"}, "", errors.New("transient"))
	name, err := resolveRepoName(context.Background(), io.Discard, io.Discard, r, testUser, t.TempDir(), GitHubBootstrapOptions{RepoName: "ok-name"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "ok-name" {
		t.Fatalf("name = %q", name)
	}
}

func TestResolveRepoName_FlagRejectsInvalid(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	_, err := resolveRepoName(context.Background(), io.Discard, io.Discard, r, testUser, t.TempDir(), GitHubBootstrapOptions{RepoName: "has/slash"})
	if err == nil {
		t.Fatal("expected error for name containing '/'")
	}
}

func TestGhRepoExists_RealErrorPath(t *testing.T) {
	t.Parallel()
	// If `gh repo view` succeeds (no error), the repo exists.
	r := newFakeRunner()
	r.set("gh", []string{"repo", "view", "octocat/real", "--json", "name"}, "{\"name\":\"real\"}", nil)
	exists, err := ghRepoExists(context.Background(), r, testUser, "real")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
}

func TestDoInitialCommit_EmptyFolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := newFakeRunner()
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"status", "--porcelain"}, "", nil)

	committed, err := doInitialCommit(context.Background(), r, dir, "msg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if committed {
		t.Fatal("expected committed=false for empty folder")
	}
}

func TestDoInitialCommit_WithFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := newFakeRunner()
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"status", "--porcelain"}, " M README.md\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "msg"}, "", nil)

	committed, err := doInitialCommit(context.Background(), r, dir, "msg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !committed {
		t.Fatal("expected committed=true")
	}
	// Verify gpgsign=false was passed to the commit.
	if !r.hasCall(func(c fakeCall) bool {
		return c.name == cmdGit && len(c.args) >= 3 && c.args[0] == "-c" && c.args[1] == "commit.gpgsign=false" && c.args[2] == "commit"
	}) {
		t.Fatal("expected commit to pass -c commit.gpgsign=false")
	}
}

func TestRunGitHubBootstrap_DeclinedInNonInteractive(t *testing.T) {
	t.Setenv("ENTIRE_TEST_TTY", "0")
	dir := t.TempDir()
	restoreCwd(t, dir)

	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, GitHubBootstrapOptions{}, newFakeRunner())
	if !errors.Is(err, errBootstrapDeclined) {
		t.Fatalf("expected errBootstrapDeclined, got %v", err)
	}
}

func TestRunGitHubBootstrap_NoGitHubFlow(t *testing.T) {
	t.Setenv("ENTIRE_TEST_TTY", "0")
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"status", "--porcelain"}, " M file\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "First!"}, "", nil)

	opts := GitHubBootstrapOptions{
		InitRepo:             true,
		NoGitHub:             true,
		InitialCommitMessage: "First!",
	}
	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git init ran in the cwd.
	if !r.hasCall(func(c fakeCall) bool {
		return c.name == cmdGit && len(c.args) == 1 && c.args[0] == "init"
	}) {
		t.Fatal("expected git init call")
	}
	// Verify no gh calls were made.
	if r.hasCall(func(c fakeCall) bool { return c.name == "gh" }) {
		t.Fatal("did not expect gh calls with --no-github")
	}
}

func TestRunGitHubBootstrap_GhMissingFallsBackToLocal(t *testing.T) {
	t.Setenv("ENTIRE_TEST_TTY", "0")
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("gh", []string{"--version"}, "", errors.New("not found"))
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"status", "--porcelain"}, "", nil)

	opts := GitHubBootstrapOptions{InitRepo: true}
	var errBuf bytes.Buffer
	err := runGitHubBootstrapWith(context.Background(), io.Discard, &errBuf, opts, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(errBuf.String(), "gh CLI not found") {
		t.Fatalf("expected hint about installing gh, got %q", errBuf.String())
	}
}

func TestRunGitHubBootstrap_FullNonInteractive(t *testing.T) {
	t.Setenv("ENTIRE_TEST_TTY", "0")
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("gh", []string{"--version"}, "gh 2.81.0", nil)
	r.set("gh", []string{"auth", "status"}, "Logged in", nil)
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "octocat\n", nil)
	r.set("gh", []string{"api", "user/orgs", "--jq", ".[].login"}, "", nil)
	// Name availability check: repo does not exist yet.
	r.set("gh", []string{"repo", "view", "octocat/my-new", "--json", "name"}, "", errors.New("not found"))
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"status", "--porcelain"}, " M f\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "Seed"}, "", nil)
	r.setInteractive("gh", []string{
		"repo", "create", "octocat/my-new",
		"--private",
		"--source=.",
		"--remote=origin",
		"--push",
	}, nil)

	opts := GitHubBootstrapOptions{
		InitRepo:             true,
		RepoName:             "my-new",
		RepoVisibility:       "private",
		InitialCommitMessage: "Seed",
	}
	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !r.hasCall(func(c fakeCall) bool {
		return c.name == "gh" && len(c.args) > 3 && c.args[0] == "repo" && c.args[1] == "create"
	}) {
		t.Fatal("expected gh repo create call")
	}
}

func TestRunGitHubBootstrap_RepoExistsFails(t *testing.T) {
	t.Setenv("ENTIRE_TEST_TTY", "0")
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.set("gh", []string{"--version"}, "gh", nil)
	r.set("gh", []string{"auth", "status"}, "", nil)
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "octocat\n", nil)
	r.set("gh", []string{"api", "user/orgs", "--jq", ".[].login"}, "", nil)
	// The name is already taken. Since we aren't returning an *exec.ExitError,
	// ghRepoExists returns (false, err) and ghRepoExists wraps. To avoid
	// plumbing ExitError into the test, use the "already exists" path directly
	// by returning success — meaning the repo was found.
	r.set("gh", []string{"repo", "view", "octocat/taken", "--json", "name"}, "{\"name\":\"taken\"}", nil)
	r.set("git", []string{"init"}, "", nil)

	opts := GitHubBootstrapOptions{
		InitRepo: true,
		RepoName: "taken",
	}
	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, r)
	if err == nil {
		t.Fatal("expected error when repo already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error, got %v", err)
	}
}

func TestEnsureGitIdentity_AlreadyConfigured(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.setIdentityConfigured()

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No git config writes should have occurred.
	if r.hasCall(func(c fakeCall) bool {
		return c.name == cmdGit && len(c.args) >= 2 && c.args[0] == "config" && (c.args[1] == "user.name" || c.args[1] == "user.email")
	}) {
		t.Fatal("did not expect identity writes when already configured")
	}
}

func TestEnsureGitIdentity_SourcedFromGh(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// Identity missing locally (empty stdout).
	r.set("git", []string{"config", "--get", "user.name"}, "", errors.New("not set"))
	r.set("git", []string{"config", "--get", "user.email"}, "", errors.New("not set"))
	// gh available and authenticated.
	r.set("gh", []string{"--version"}, "gh", nil)
	r.set("gh", []string{"auth", "status"}, "ok", nil)
	r.set("gh", []string{"api", "user"}, `{"id":42,"login":"octo","name":"Octo Cat","email":"octo@example.com"}`, nil)
	// Expect writes with values from gh.
	r.set("git", []string{"config", "user.name", "Octo Cat"}, "", nil)
	r.set("git", []string{"config", "user.email", "octo@example.com"}, "", nil)

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureGitIdentity_GhNoreplyFallback(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("git", []string{"config", "--get", "user.name"}, "", errors.New("not set"))
	r.set("git", []string{"config", "--get", "user.email"}, "", errors.New("not set"))
	r.set("gh", []string{"--version"}, "gh", nil)
	r.set("gh", []string{"auth", "status"}, "ok", nil)
	// email is null/missing: should fall back to id+login noreply.
	r.set("gh", []string{"api", "user"}, `{"id":42,"login":"octo","name":"","email":null}`, nil)
	r.set("git", []string{"config", "user.name", "octo"}, "", nil)
	r.set("git", []string{"config", "user.email", "42+octo@users.noreply.github.com"}, "", nil)

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureGitIdentity_NonInteractiveNoGh_Errors(t *testing.T) {
	t.Setenv("ENTIRE_TEST_TTY", "0")
	r := newFakeRunner()
	r.set("git", []string{"config", "--get", "user.name"}, "", errors.New("not set"))
	r.set("git", []string{"config", "--get", "user.email"}, "", errors.New("not set"))
	r.set("gh", []string{"--version"}, "", errors.New("not found"))

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err == nil {
		t.Fatal("expected error when identity missing and gh unavailable")
	}
	if !strings.Contains(err.Error(), "git config --global user.name") {
		t.Fatalf("expected guidance to set git config, got %v", err)
	}
}

func TestGhUserIdentity_NameFallsBackToLogin(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("gh", []string{"api", "user"}, `{"id":7,"login":"dev","name":"","email":"dev@example.com"}`, nil)
	name, email, err := ghUserIdentity(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "dev" {
		t.Fatalf("name = %q", name)
	}
	if email != "dev@example.com" {
		t.Fatalf("email = %q", email)
	}
}

// TestBootstrap_FreshMachine_RealGit is an integration-style test that runs
// real git via execRunner on a temp dir isolated from the user's global git
// config. Regression guard for the issue where bootstrap commits failed
// without a configured identity or because of commit.gpgsign=true.
func TestBootstrap_FreshMachine_RealGit(t *testing.T) {
	t.Setenv("ENTIRE_TEST_TTY", "0")

	// Isolate from any global git config: point HOME + GIT_CONFIG_* at
	// empty/missing locations, and force a broken GPG signing config that
	// would fail any commit if we did not pass -c commit.gpgsign=false.
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	// A global config that demands signing with a non-existent program. If
	// our bootstrap did not override gpgsign for its commit, git would
	// error out here.
	globalCfg := filepath.Join(emptyHome, ".gitconfig")
	globalContent := "[user]\n\tname = Fresh User\n\temail = fresh@example.com\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /does/not/exist\n"
	if err := writeTempFile(globalCfg, globalContent); err != nil {
		t.Fatalf("write global gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	// Ensure no system config interferes.
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	projectDir := t.TempDir()
	restoreCwd(t, projectDir)
	// Create a file to commit.
	if err := writeTempFile(filepath.Join(projectDir, "README.md"), "hello\n"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	opts := GitHubBootstrapOptions{
		InitRepo:             true,
		NoGitHub:             true,
		InitialCommitMessage: "Initial",
	}
	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, execRunner{})
	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	// Verify a commit actually landed on HEAD.
	out, err := execRunner{}.RunInDir(context.Background(), projectDir, "git", "log", "--oneline")
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if !strings.Contains(out, "Initial") {
		t.Fatalf("expected 'Initial' commit in log, got: %q", out)
	}
}

func writeTempFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// TestBootstrap_FreshMachine_NoIdentity_RealGit verifies that a fresh machine
// without any git identity configured fails cleanly in non-interactive mode
// with a helpful error message, instead of letting git commit fail with a
// confusing "please tell me who you are" stderr.
func TestBootstrap_FreshMachine_NoIdentity_RealGit(t *testing.T) {
	t.Setenv("ENTIRE_TEST_TTY", "0")

	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	// Empty global config: no user.name/user.email.
	globalCfg := filepath.Join(emptyHome, ".gitconfig")
	if err := writeTempFile(globalCfg, ""); err != nil {
		t.Fatalf("write global gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	// Ensure gh isn't authenticated for the purpose of this test — point
	// PATH at an empty directory so `gh` resolves to "not found".
	emptyBin := t.TempDir()
	t.Setenv("PATH", emptyBin)

	projectDir := t.TempDir()
	restoreCwd(t, projectDir)
	if err := writeTempFile(filepath.Join(projectDir, "README.md"), "hi\n"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	opts := GitHubBootstrapOptions{
		InitRepo:             true,
		NoGitHub:             true,
		InitialCommitMessage: "x",
	}
	// With PATH wiped, execRunner can't find git either — so use a runner
	// that keeps git on the original PATH but points gh to nowhere. The
	// simplest portable way: re-extend PATH with common git locations.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin")

	err := runGitHubBootstrapWith(context.Background(), io.Discard, io.Discard, opts, execRunner{})
	if err == nil {
		t.Fatal("expected error when identity missing and gh unavailable")
	}
	if !strings.Contains(err.Error(), "git config --global user.name") {
		t.Fatalf("expected guidance to set git config, got: %v", err)
	}
}

func TestEnableCmd_InitRepoFlagsMutuallyExclusive(t *testing.T) {
	setupTestRepo(t)

	cmd := newEnableCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--init-repo", "--no-init-repo"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --init-repo and --no-init-repo are set")
	}
	if !strings.Contains(err.Error(), "init-repo") || !strings.Contains(err.Error(), "no-init-repo") {
		t.Fatalf("expected error to mention both flags, got: %v", err)
	}
}

// restoreCwd chdirs into dir for the duration of the test.
func restoreCwd(t *testing.T, dir string) {
	t.Helper()
	// macOS resolves /tmp → /private/tmp; canonicalize for safety.
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		canon = dir
	}
	t.Chdir(canon)
}
