package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/entireio/cli/cmd/entire/cli/interactive"
	"github.com/entireio/cli/cmd/entire/cli/paths"
)

// GitHubBootstrapOptions holds flags that let `entire enable` run on a folder
// that isn't yet a git repository. All fields are optional; supplying one
// skips the matching interactive prompt.
type GitHubBootstrapOptions struct {
	// InitRepo is true if --init-repo was passed (accept git init without prompt).
	InitRepo bool
	// NoInitRepo is true if --no-init-repo was passed (decline without prompt).
	NoInitRepo bool
	// RepoName is the GitHub repository name (no owner).
	RepoName string
	// RepoOwner is the GitHub user or org login.
	RepoOwner string
	// RepoVisibility is one of "public", "private", "internal".
	RepoVisibility string
	// NoGitHub skips the GitHub repo creation step.
	NoGitHub bool
	// InitialCommitMessage overrides the default commit message prompt.
	InitialCommitMessage string
}

// bootstrapRunner executes external commands. Tests override this to avoid
// shelling out to git/gh.
type bootstrapRunner interface {
	// Run executes the command and returns stdout. Stderr is discarded.
	Run(ctx context.Context, name string, args ...string) (string, error)
	// RunInDir is Run with an explicit working directory.
	RunInDir(ctx context.Context, dir, name string, args ...string) (string, error)
	// RunInteractive streams stdin/stdout/stderr so tools like `gh repo create`
	// can print their own progress. Returns an error on non-zero exit.
	RunInteractive(ctx context.Context, dir, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func (execRunner) RunInDir(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func (execRunner) RunInteractive(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

// errBootstrapDeclined signals that the user chose not to initialize a
// repo. Returned _before_ `git init` runs; callers fall back to the
// legacy "Not a git repository" error.
var errBootstrapDeclined = errors.New("bootstrap declined")

// errBootstrapInterrupted signals that the user aborted a prompt _after_
// `git init` has already run. The local repo is in place but setup
// didn't complete; callers should surface that clearly instead of
// pretending no init happened.
var errBootstrapInterrupted = errors.New("bootstrap interrupted after init")

// ghRepoNameRe validates GitHub repository names. GitHub allows alphanumerics,
// hyphens, underscores, and periods; it does not allow spaces or leading dots.
var ghRepoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// allowed visibility values.
const (
	visibilityPublic   = "public"
	visibilityPrivate  = "private"
	visibilityInternal = "internal"
)

// bootstrapState carries pre-setup decisions into the post-setup finalize
// step. The caller runs `runGitHubBootstrapInit` before agent setup to do
// `git init` + identity + gather GitHub choices, then runs
// `runGitHubBootstrapFinalize` afterwards so the initial commit captures
// the `.entire/`, `.claude/`, etc. files written during setup.
type bootstrapState struct {
	runner     bootstrapRunner
	cwd        string
	useGitHub  bool
	fullName   string // owner/name, if useGitHub
	visibility string // public/private/internal, if useGitHub
	message    string // resolved initial commit message
}

// runGitHubBootstrapInit handles the pre-setup half of "enable on a non-git
// folder": confirm + `git init`, ensure git identity, and (if we're going
// to create a GitHub repo) gather owner/name/visibility up front so all
// prompts happen before agent setup runs.
//
// Returns errBootstrapDeclined if the user declined the init prompt.
// Returns nil, nil if the caller is already inside a git repo and no
// bootstrap is needed (defensive; the caller typically gates on this).
func runGitHubBootstrapInit(ctx context.Context, w, errW io.Writer, opts GitHubBootstrapOptions) (*bootstrapState, error) {
	return runGitHubBootstrapInitWith(ctx, w, errW, opts, execRunner{})
}

// runGitHubBootstrapInitWith is the testable variant that accepts a runner.
func runGitHubBootstrapInitWith(ctx context.Context, w, errW io.Writer, opts GitHubBootstrapOptions, runner bootstrapRunner) (*bootstrapState, error) {
	// paths.RepoRoot is unavailable here — we're bootstrapping _before_ a
	// repo exists. Plain cwd is the correct target for `git init`.
	cwd, err := os.Getwd() //nolint:forbidigo // no repo yet; git init runs in cwd
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	// Step 1: confirm we should git init here.
	proceed, err := confirmInitRepo(w, cwd, opts)
	if err != nil {
		return nil, err
	}
	if !proceed {
		return nil, errBootstrapDeclined
	}

	// Step 2: git init.
	if err := gitInit(ctx, runner, cwd); err != nil {
		return nil, fmt.Errorf("git init: %w", err)
	}
	// Clear cached worktree root so subsequent paths.WorktreeRoot calls pick
	// up the freshly created repo.
	paths.ClearWorktreeRootCache()
	fmt.Fprintln(w, "Initialized empty Git repository.")

	// Step 3: decide whether to create a GitHub repo. If gh is missing or the
	// user passed --no-github, we skip that branch but still bootstrap the
	// local repo.
	useGitHub := !opts.NoGitHub
	if useGitHub {
		if !ghAvailable(ctx, runner) {
			fmt.Fprintln(errW, "gh CLI not found. Install it from https://cli.github.com/ and run `gh auth login` to add a GitHub remote.")
			fmt.Fprintln(errW, "Continuing with local initialization only.")
			useGitHub = false
		} else if !ghAuthenticated(ctx, runner) {
			fmt.Fprintln(errW, "gh CLI is not authenticated. Run `gh auth login` to add a GitHub remote.")
			fmt.Fprintln(errW, "Continuing with local initialization only.")
			useGitHub = false
		}
	}

	// Step 3b: ask a simple yes/no before diving into owner/name/visibility
	// prompts. Skip the confirm when any gh-specific flag is set (the flag
	// implies intent) or when we're non-interactive (keep the documented
	// happy path: default to yes).
	if useGitHub && !ghFlagsProvided(opts) && interactive.CanPromptInteractively() {
		confirmed, err := confirmCreateGitHubRepo()
		if err != nil {
			return nil, err
		}
		if !confirmed {
			useGitHub = false
		}
	}

	// Step 4: collect GitHub repo details up front so all prompts are
	// contiguous.
	var fullName, visibility string
	if useGitHub {
		owner, name, vis, err := selectGitHubRepo(ctx, w, errW, runner, cwd, opts)
		if err != nil {
			return nil, err
		}
		fullName = owner + "/" + name
		visibility = vis
	}

	// Step 5: resolve commit message and ensure git identity. Must run after
	// `git init` so `git config` reads are scoped correctly.
	message, err := resolveCommitMessage(w, opts)
	if err != nil {
		return nil, err
	}
	if err := ensureGitIdentity(ctx, w, errW, runner, cwd); err != nil {
		return nil, err
	}

	return &bootstrapState{
		runner:     runner,
		cwd:        cwd,
		useGitHub:  useGitHub,
		fullName:   fullName,
		visibility: visibility,
		message:    message,
	}, nil
}

// runGitHubBootstrapWith runs the full bootstrap (init + finalize) in one
// call, used by tests that don't need to assert phasing. The real caller
// runs the two phases around agent setup.
func runGitHubBootstrapWith(ctx context.Context, w, errW io.Writer, opts GitHubBootstrapOptions, runner bootstrapRunner) error {
	state, err := runGitHubBootstrapInitWith(ctx, w, errW, opts, runner)
	if err != nil {
		return err
	}
	return runGitHubBootstrapFinalize(ctx, w, state)
}

// runGitHubBootstrapFinalize runs the post-setup half: stage + initial
// commit (now including any `.entire/`, agent hook, and settings files
// written by the enable flow), then create the GitHub repo and push.
func runGitHubBootstrapFinalize(ctx context.Context, w io.Writer, s *bootstrapState) error {
	if s == nil {
		return nil
	}
	committed, err := doInitialCommit(ctx, s.runner, s.cwd, s.message)
	if err != nil {
		return fmt.Errorf("initial commit: %w", err)
	}
	if !committed {
		fmt.Fprintln(w, "No files to commit; skipping initial commit.")
	}
	if s.useGitHub {
		if err := ghRepoCreate(ctx, s.runner, s.cwd, s.fullName, s.visibility, committed); err != nil {
			return fmt.Errorf("gh repo create: %w", err)
		}
		fmt.Fprintf(w, "Created GitHub repository %s.\n", s.fullName)
	}
	return nil
}

// ghFlagsProvided reports whether the caller has already expressed intent
// to create a GitHub repo via any of the gh-specific flags. Used to skip
// the "create on GitHub?" confirm prompt in that case.
func ghFlagsProvided(opts GitHubBootstrapOptions) bool {
	return opts.RepoName != "" || opts.RepoOwner != "" || opts.RepoVisibility != ""
}

// confirmCreateGitHubRepo asks the user whether they want to also create
// a matching GitHub repository. Interactive-only; callers gate on
// CanPromptInteractively.
func confirmCreateGitHubRepo() (bool, error) {
	confirmed := true
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Create a matching repository on GitHub?").
				Value(&confirmed),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, errBootstrapInterrupted
		}
		return false, fmt.Errorf("github confirm prompt: %w", err)
	}
	return confirmed, nil
}

// confirmInitRepo returns true if we should proceed with `git init`. It
// respects --init-repo / --no-init-repo; otherwise prompts.
func confirmInitRepo(w io.Writer, cwd string, opts GitHubBootstrapOptions) (bool, error) {
	if opts.NoInitRepo {
		return false, nil
	}
	if opts.InitRepo {
		return true, nil
	}
	if !interactive.CanPromptInteractively() {
		fmt.Fprintln(w, "Not a git repository. Pass --init-repo to initialize one non-interactively.")
		return false, nil
	}

	folder := filepath.Base(cwd)
	confirmed := true
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("No git repository in %q. Initialize one here?", folder)).
				Value(&confirmed),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("init-repo prompt: %w", err)
	}
	return confirmed, nil
}

// selectGitHubRepo gathers owner, repo name, and visibility, respecting
// supplied flags and falling back to interactive prompts.
func selectGitHubRepo(ctx context.Context, w, errW io.Writer, runner bootstrapRunner, cwd string, opts GitHubBootstrapOptions) (owner, name, visibility string, err error) {
	currentUser, userErr := ghCurrentUser(ctx, runner)
	if userErr != nil {
		return "", "", "", fmt.Errorf("query current gh user: %w", userErr)
	}
	orgs, orgErr := ghListOrgs(ctx, runner)
	if orgErr != nil {
		// Missing read:org scope is non-fatal — we can still offer the user
		// account. Warn and continue.
		fmt.Fprintf(errW, "Warning: could not list organizations (%v). Only your user account is available.\n", orgErr)
		orgs = nil
	}

	owner, err = resolveOwner(w, currentUser, orgs, opts)
	if err != nil {
		return "", "", "", err
	}

	name, err = resolveRepoName(ctx, w, errW, runner, owner, cwd, opts)
	if err != nil {
		return "", "", "", err
	}

	visibility, err = resolveVisibility(w, owner, currentUser, opts)
	if err != nil {
		return "", "", "", err
	}

	return owner, name, visibility, nil
}

func resolveOwner(w io.Writer, currentUser string, orgs []string, opts GitHubBootstrapOptions) (string, error) {
	owners := append([]string{currentUser}, orgs...)
	if opts.RepoOwner != "" {
		for _, candidate := range owners {
			if candidate == opts.RepoOwner {
				return opts.RepoOwner, nil
			}
		}
		// Owner not in known list — allow it anyway; gh repo create will
		// error out later if invalid. This supports orgs the token can't
		// enumerate (e.g. missing read:org scope).
		return opts.RepoOwner, nil
	}
	if len(owners) == 1 {
		fmt.Fprintf(w, "Using GitHub owner: %s\n", currentUser)
		return currentUser, nil
	}
	if !interactive.CanPromptInteractively() {
		return currentUser, nil
	}

	options := make([]huh.Option[string], 0, len(owners))
	for _, o := range owners {
		options = append(options, huh.NewOption(o, o))
	}
	selected := currentUser
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Choose the GitHub owner for the new repository").
				Options(options...).
				Value(&selected),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", errBootstrapInterrupted
		}
		return "", fmt.Errorf("owner prompt: %w", err)
	}
	return selected, nil
}

func resolveRepoName(ctx context.Context, w, errW io.Writer, runner bootstrapRunner, owner, cwd string, opts GitHubBootstrapOptions) (string, error) {
	suggested := slugifyRepoName(filepath.Base(cwd))

	if opts.RepoName != "" {
		if err := validateRepoName(opts.RepoName); err != nil {
			return "", err
		}
		exists, checkErr := ghRepoExists(ctx, runner, owner, opts.RepoName)
		if checkErr != nil {
			fmt.Fprintf(errW, "Warning: could not check if %s/%s already exists (%v).\n", owner, opts.RepoName, checkErr)
		} else if exists {
			return "", fmt.Errorf("repository %s/%s already exists on GitHub", owner, opts.RepoName)
		}
		return opts.RepoName, nil
	}

	if !interactive.CanPromptInteractively() {
		return suggested, nil
	}

	name := suggested
	for {
		var input string
		form := NewAccessibleForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Repository name").
					Description(fmt.Sprintf("Press enter to use %q", name)).
					Value(&input),
			),
		)
		if err := form.Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return "", errBootstrapInterrupted
			}
			return "", fmt.Errorf("repo name prompt: %w", err)
		}
		if strings.TrimSpace(input) != "" {
			name = strings.TrimSpace(input)
		}
		if err := validateRepoName(name); err != nil {
			fmt.Fprintf(errW, "Invalid name: %v\n", err)
			continue
		}
		exists, checkErr := ghRepoExists(ctx, runner, owner, name)
		if checkErr != nil {
			fmt.Fprintf(errW, "Warning: could not check if %s/%s already exists (%v). Proceeding; gh will error out if it is taken.\n", owner, name, checkErr)
			return name, nil
		}
		if exists {
			fmt.Fprintf(w, "%s/%s already exists on GitHub. Pick a different name.\n", owner, name)
			continue
		}
		return name, nil
	}
}

func resolveVisibility(w io.Writer, owner, currentUser string, opts GitHubBootstrapOptions) (string, error) {
	isOrg := owner != currentUser

	if opts.RepoVisibility != "" {
		vis := strings.ToLower(opts.RepoVisibility)
		switch vis {
		case visibilityPublic, visibilityPrivate:
			return vis, nil
		case visibilityInternal:
			if !isOrg {
				return "", errors.New("visibility 'internal' is only available for organization repositories")
			}
			return vis, nil
		default:
			return "", fmt.Errorf("invalid visibility %q: must be one of public, private, internal", opts.RepoVisibility)
		}
	}
	if !interactive.CanPromptInteractively() {
		return visibilityPrivate, nil
	}

	options := []huh.Option[string]{
		huh.NewOption("Private", visibilityPrivate),
		huh.NewOption("Public", visibilityPublic),
	}
	if isOrg {
		options = append(options, huh.NewOption("Internal", visibilityInternal))
	}
	selected := visibilityPrivate
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Repository visibility").
				Options(options...).
				Value(&selected),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", errBootstrapInterrupted
		}
		return "", fmt.Errorf("visibility prompt: %w", err)
	}
	_ = w
	return selected, nil
}

func resolveCommitMessage(w io.Writer, opts GitHubBootstrapOptions) (string, error) {
	if opts.InitialCommitMessage != "" {
		return opts.InitialCommitMessage, nil
	}
	defaultMsg := "Initial commit"
	if !interactive.CanPromptInteractively() {
		return defaultMsg, nil
	}
	input := defaultMsg
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Initial commit message").
				Value(&input),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", errBootstrapInterrupted
		}
		return "", fmt.Errorf("commit message prompt: %w", err)
	}
	_ = w
	if strings.TrimSpace(input) == "" {
		return defaultMsg, nil
	}
	return input, nil
}

// gitInit runs `git init` in the given directory.
func gitInit(ctx context.Context, runner bootstrapRunner, dir string) error {
	if _, err := runner.RunInDir(ctx, dir, "git", "init"); err != nil {
		return fmt.Errorf("run git init: %w", err)
	}
	return nil
}

// doInitialCommit stages all files and creates a commit. Returns whether a
// commit was actually created (false if there were no files to stage).
func doInitialCommit(ctx context.Context, runner bootstrapRunner, dir, message string) (bool, error) {
	if _, err := runner.RunInDir(ctx, dir, "git", "add", "-A"); err != nil {
		return false, fmt.Errorf("git add: %w", err)
	}
	// Check if the staging area has anything at all.
	out, err := runner.RunInDir(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	// Disable GPG signing for this commit only. Fresh environments often
	// have commit.gpgsign=true inherited from a global config but no
	// working signer; passing -c keeps the user's global config intact.
	if _, err := runner.RunInDir(ctx, dir, "git", "-c", "commit.gpgsign=false", "commit", "-m", message); err != nil {
		return false, fmt.Errorf("git commit: %w", err)
	}
	return true, nil
}

// ensureGitIdentity guarantees the repo has a user.name/user.email set at
// some scope. If neither is configured, we source values from `gh api user`
// when available, otherwise prompt (interactive) or fail with a helpful
// message (non-interactive). Values are written to the local repo config
// only, so the user's global state is never mutated.
func ensureGitIdentity(ctx context.Context, w, errW io.Writer, runner bootstrapRunner, dir string) error {
	// `git config --get` exits non-zero when the key isn't set. Treat any
	// error as "unset" rather than fatal so we can fall through to sourcing
	// the identity from elsewhere.
	existingName, nameErr := runner.RunInDir(ctx, dir, "git", "config", "--get", "user.name")
	existingEmail, emailErr := runner.RunInDir(ctx, dir, "git", "config", "--get", "user.email")
	if nameErr == nil && emailErr == nil && strings.TrimSpace(existingName) != "" && strings.TrimSpace(existingEmail) != "" {
		return nil
	}

	var ghName, ghEmail string
	if ghAvailable(ctx, runner) && ghAuthenticated(ctx, runner) {
		if n, e, err := ghUserIdentity(ctx, runner); err == nil {
			ghName, ghEmail = n, e
		}
	}

	name, email, err := resolveGitIdentity(w, errW, ghName, ghEmail)
	if err != nil {
		return err
	}

	if _, err := runner.RunInDir(ctx, dir, "git", "config", "user.name", name); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if _, err := runner.RunInDir(ctx, dir, "git", "config", "user.email", email); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}
	return nil
}

// resolveGitIdentity returns the name/email to use, prompting interactively
// when values aren't already provided by gh.
func resolveGitIdentity(w, _ io.Writer, ghName, ghEmail string) (string, string, error) {
	if ghName != "" && ghEmail != "" {
		fmt.Fprintf(w, "Using git identity from gh: %s <%s>\n", ghName, ghEmail)
		return ghName, ghEmail, nil
	}
	if !interactive.CanPromptInteractively() {
		return "", "", errors.New(`git identity not configured. Set it with:
  git config --global user.name "Your Name"
  git config --global user.email "you@example.com"`)
	}

	name := ghName
	email := ghEmail
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewInput().Title("Git user.name").Value(&name),
			huh.NewInput().Title("Git user.email").Value(&email),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", "", errBootstrapInterrupted
		}
		return "", "", fmt.Errorf("git identity prompt: %w", err)
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
		return "", "", errors.New("git user.name and user.email are both required")
	}
	return strings.TrimSpace(name), strings.TrimSpace(email), nil
}

// ghUserResponse is the subset of `gh api user` fields we care about.
type ghUserResponse struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ghUserIdentity returns a best-effort (name, email) from `gh api user`.
// Missing name falls back to login; missing email falls back to the GitHub
// no-reply address, which is always accepted by GitHub.
func ghUserIdentity(ctx context.Context, runner bootstrapRunner) (string, string, error) {
	out, err := runner.Run(ctx, "gh", "api", "user")
	if err != nil {
		return "", "", fmt.Errorf("gh api user: %w", err)
	}
	var resp ghUserResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", "", fmt.Errorf("parse gh user response: %w", err)
	}
	name := resp.Name
	if name == "" {
		name = resp.Login
	}
	email := resp.Email
	if email == "" && resp.ID != 0 && resp.Login != "" {
		email = fmt.Sprintf("%d+%s@users.noreply.github.com", resp.ID, resp.Login)
	}
	if name == "" || email == "" {
		return "", "", errors.New("gh user response missing identity fields")
	}
	return name, email, nil
}

// ghAvailable reports whether the gh CLI is installed.
func ghAvailable(ctx context.Context, runner bootstrapRunner) bool {
	_, err := runner.Run(ctx, "gh", "--version")
	return err == nil
}

// ghAuthenticated reports whether `gh auth status` succeeds.
func ghAuthenticated(ctx context.Context, runner bootstrapRunner) bool {
	_, err := runner.Run(ctx, "gh", "auth", "status")
	return err == nil
}

// ghCurrentUser returns the authenticated GitHub user's login.
func ghCurrentUser(ctx context.Context, runner bootstrapRunner) (string, error) {
	out, err := runner.Run(ctx, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return "", fmt.Errorf("gh api user: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// ghListOrgs returns the orgs the authenticated user belongs to, sorted
// alphabetically. Requires the `read:org` token scope.
func ghListOrgs(ctx context.Context, runner bootstrapRunner) ([]string, error) {
	out, err := runner.Run(ctx, "gh", "api", "user/orgs", "--jq", ".[].login")
	if err != nil {
		return nil, fmt.Errorf("gh api user/orgs: %w", err)
	}
	var orgs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			orgs = append(orgs, line)
		}
	}
	sort.Strings(orgs)
	return orgs, nil
}

// ghRepoExists checks whether <owner>/<name> exists on GitHub.
func ghRepoExists(ctx context.Context, runner bootstrapRunner, owner, name string) (bool, error) {
	_, err := runner.Run(ctx, "gh", "repo", "view", owner+"/"+name, "--json", "name")
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		msg := string(exitErr.Stderr)
		if strings.Contains(msg, "Could not resolve") || strings.Contains(msg, "not found") || strings.Contains(msg, "Not Found") {
			return false, nil
		}
	}
	return false, fmt.Errorf("gh repo view: %w", err)
}

// ghRepoCreate creates a GitHub repo from the local source directory, adds
// origin as its remote, and pushes if there's anything to push.
func ghRepoCreate(ctx context.Context, runner bootstrapRunner, dir, fullName, visibility string, hasCommits bool) error {
	// Create the remote repo and add origin, but don't push yet. We push
	// separately below with --no-verify so the pre-push hook doesn't run
	// on this first push: the entire/checkpoints/v1 branch has nothing to
	// checkpoint (no sessions yet), and if it's pushed alongside the
	// default branch GitHub can pick it as the default.
	args := []string{
		"repo", "create", fullName,
		"--" + visibility,
		"--source=.",
		"--remote=origin",
	}
	if err := runner.RunInteractive(ctx, dir, "gh", args...); err != nil {
		return fmt.Errorf("gh repo create: %w", err)
	}
	if hasCommits {
		if err := runner.RunInteractive(ctx, dir, "git", "push", "--no-verify", "-u", "origin", "HEAD"); err != nil {
			return fmt.Errorf("git push: %w", err)
		}
	}
	return nil
}

// slugifyRepoName turns a folder name into a GitHub-safe repo name. Invalid
// characters are replaced with '-', and runs of '-' are collapsed.
func slugifyRepoName(folder string) string {
	var b strings.Builder
	b.Grow(len(folder))
	for _, r := range folder {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	slug := b.String()
	// Collapse repeated dashes.
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-.")
	if slug == "" {
		slug = "my-repo"
	}
	return slug
}

// validateRepoName checks whether name is a valid GitHub repo name.
func validateRepoName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > 100 {
		return errors.New("name must be at most 100 characters")
	}
	if strings.Contains(name, "/") {
		return errors.New("name must not contain '/' (pass --repo-owner separately)")
	}
	if !ghRepoNameRe.MatchString(name) {
		return errors.New("name may only contain letters, digits, '.', '-', '_' and must start with a letter or digit")
	}
	return nil
}
