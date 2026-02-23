package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
	"github.com/entireio/cli/cmd/entire/cli/trail"

	"github.com/charmbracelet/huh"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/cobra"
)

func newTrailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trail",
		Short: "Manage trails for your branches",
		Long: `Trails are branch-centric work tracking abstractions. They describe the
"why" and "what" of your work, while checkpoints capture the "how" and "when".

Running 'entire trail' without a subcommand shows the trail for the current
branch, or lists all trails if no trail exists for the current branch.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTrailShow(cmd.OutOrStdout())
		},
	}

	cmd.AddCommand(newTrailListCmd())
	cmd.AddCommand(newTrailCreateCmd())
	cmd.AddCommand(newTrailUpdateCmd())

	return cmd
}

// runTrailShow shows the trail for the current branch, or falls through to list.
func runTrailShow(w io.Writer) error {
	branch, err := GetCurrentBranch()
	if err != nil {
		return runTrailListAll(w, "", false)
	}

	repo, err := strategy.OpenRepository()
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	store := trail.NewStore(repo)
	metadata, err := store.FindByBranch(branch)
	if err != nil || metadata == nil {
		return runTrailListAll(w, "", false)
	}

	printTrailDetails(w, metadata)
	return nil
}

func printTrailDetails(w io.Writer, m *trail.Metadata) {
	fmt.Fprintf(w, "Trail: %s\n", m.Title)
	fmt.Fprintf(w, "  ID:      %s\n", m.TrailID)
	fmt.Fprintf(w, "  Branch:  %s\n", m.Branch)
	fmt.Fprintf(w, "  Base:    %s\n", m.Base)
	fmt.Fprintf(w, "  Status:  %s\n", m.Status)
	fmt.Fprintf(w, "  Author:  %s\n", m.Author)
	if m.Description != "" {
		fmt.Fprintf(w, "  Description: %s\n", m.Description)
	}
	if len(m.Labels) > 0 {
		fmt.Fprintf(w, "  Labels:  %s\n", strings.Join(m.Labels, ", "))
	}
	if len(m.Assignees) > 0 {
		fmt.Fprintf(w, "  Assignees: %s\n", strings.Join(m.Assignees, ", "))
	}
	fmt.Fprintf(w, "  Created: %s\n", m.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "  Updated: %s\n", m.UpdatedAt.Format(time.RFC3339))
}

func newTrailListCmd() *cobra.Command {
	var statusFilter string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all trails",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTrailListAll(cmd.OutOrStdout(), statusFilter, jsonOutput)
		},
	}

	cmd.Flags().StringVar(&statusFilter, "status", "", "Filter by status (draft, open, in_progress, in_review, done, closed)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func runTrailListAll(w io.Writer, statusFilter string, jsonOutput bool) error {
	repo, err := strategy.OpenRepository()
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	store := trail.NewStore(repo)
	trails, err := store.List()
	if err != nil {
		return fmt.Errorf("failed to list trails: %w", err)
	}

	if trails == nil {
		trails = []*trail.Metadata{}
	}

	// Apply status filter
	if statusFilter != "" {
		status := trail.Status(statusFilter)
		if !status.IsValid() {
			return fmt.Errorf("invalid status %q: valid values are %s", statusFilter, formatValidStatuses())
		}
		var filtered []*trail.Metadata
		for _, t := range trails {
			if t.Status == status {
				filtered = append(filtered, t)
			}
		}
		trails = filtered
	}

	// Sort by updated_at descending
	sort.Slice(trails, func(i, j int) bool {
		return trails[i].UpdatedAt.After(trails[j].UpdatedAt)
	})

	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(trails); err != nil {
			return fmt.Errorf("failed to encode JSON: %w", err)
		}
		return nil
	}

	if len(trails) == 0 {
		fmt.Fprintln(w, "No trails found.")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Commands:")
		fmt.Fprintln(w, "  entire trail create   Create a trail for the current branch")
		fmt.Fprintln(w, "  entire trail list     List all trails")
		fmt.Fprintln(w, "  entire trail update   Update trail metadata")
		return nil
	}

	// Table output
	fmt.Fprintf(w, "%-30s %-40s %-13s %-15s %s\n", "BRANCH", "TITLE", "STATUS", "AUTHOR", "UPDATED")
	for _, t := range trails {
		branch := truncate(t.Branch, 30)
		title := truncate(t.Title, 40)
		fmt.Fprintf(w, "%-30s %-40s %-13s %-15s %s\n",
			branch, title, t.Status, truncate(t.Author, 15), timeAgo(t.UpdatedAt))
	}

	return nil
}

func newTrailCreateCmd() *cobra.Command {
	var title, description, base, branch, status string
	var checkout bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a trail for the current or a new branch",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTrailCreate(cmd.OutOrStdout(), cmd.ErrOrStderr(), title, description, base, branch, status, checkout)
		},
	}

	cmd.Flags().StringVar(&title, "title", "", "Trail title")
	cmd.Flags().StringVar(&description, "description", "", "Trail description")
	cmd.Flags().StringVar(&base, "base", "", "Base branch (defaults to detected default branch)")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch for the trail (defaults to current branch)")
	cmd.Flags().StringVar(&status, "status", "", "Initial status (defaults to draft)")
	cmd.Flags().BoolVar(&checkout, "checkout", false, "Check out the branch after creating it")

	return cmd
}

//nolint:cyclop // sequential steps for creating a trail — splitting would obscure the flow
func runTrailCreate(w, errW io.Writer, title, description, base, branch, statusStr string, checkout bool) error {
	repo, err := strategy.OpenRepository()
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Determine base branch
	if base == "" {
		base = strategy.GetDefaultBranchName(repo)
		if base == "" {
			base = defaultBaseBranch
		}
	}

	_, currentBranch, _ := IsOnDefaultBranch() //nolint:errcheck // best-effort detection
	interactive := !hasFlag("title") && !hasFlag("branch")

	if interactive {
		// Interactive flow: title → description → branch (derived) → status
		if err := runTrailCreateInteractive(&title, &description, &branch, &statusStr); err != nil {
			return err
		}
	} else {
		// Non-interactive: derive missing values from provided flags
		if branch == "" {
			if hasFlag("title") {
				branch = slugifyTitle(title)
			} else {
				branch = currentBranch
			}
		}
		if title == "" {
			title = trail.HumanizeBranchName(branch)
		}
	}
	if branch == "" {
		return errors.New("branch name is required")
	}

	// Create the branch if it doesn't exist
	needsCreation := branchNeedsCreation(repo, branch)
	if needsCreation {
		if err := createBranch(repo, branch); err != nil {
			return fmt.Errorf("failed to create branch %q: %w", branch, err)
		}
		fmt.Fprintf(w, "Created branch %s\n", branch)
	} else if currentBranch != branch {
		fmt.Fprintf(errW, "Note: trail will be created for branch %q (not the current branch)\n", branch)
	}

	// Check if trail already exists for this branch
	store := trail.NewStore(repo)
	existing, err := store.FindByBranch(branch)
	if err == nil && existing != nil {
		fmt.Fprintf(w, "Trail already exists for branch %q (ID: %s)\n", branch, existing.TrailID)
		return nil
	}

	// Determine status
	var trailStatus trail.Status
	if statusStr != "" {
		trailStatus = trail.Status(statusStr)
		if !trailStatus.IsValid() {
			return fmt.Errorf("invalid status %q: valid values are %s", statusStr, formatValidStatuses())
		}
	} else {
		trailStatus = trail.StatusDraft
	}

	// Generate trail ID
	trailID, err := trail.GenerateID()
	if err != nil {
		return fmt.Errorf("failed to generate trail ID: %w", err)
	}

	// Get git author
	authorName, _ := checkpoint.GetGitAuthorFromRepo(repo)

	now := time.Now()
	metadata := &trail.Metadata{
		TrailID:     trailID,
		Branch:      branch,
		Base:        base,
		Title:       title,
		Description: description,
		Status:      trailStatus,
		Author:      authorName,
		Assignees:   []string{},
		Labels:      []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := store.Write(metadata, nil, nil); err != nil {
		return fmt.Errorf("failed to create trail: %w", err)
	}

	fmt.Fprintf(w, "Created trail %q for branch %s (ID: %s)\n", title, branch, trailID)

	// Checkout the branch if requested or prompted
	if needsCreation && currentBranch != branch {
		shouldCheckout := checkout
		if !shouldCheckout && !hasFlag("checkout") {
			// Interactive: ask whether to checkout
			form := NewAccessibleForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title(fmt.Sprintf("Check out branch %s?", branch)).
						Value(&shouldCheckout),
				),
			)
			if formErr := form.Run(); formErr == nil && shouldCheckout {
				checkout = true
			}
		}
		if checkout {
			if err := CheckoutBranch(branch); err != nil {
				return fmt.Errorf("failed to checkout branch %q: %w", branch, err)
			}
			fmt.Fprintf(w, "Switched to branch %s\n", branch)
		}
	}

	return nil
}

func newTrailUpdateCmd() *cobra.Command {
	var statusStr, title, description, branch string
	var labelAdd, labelRemove []string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update trail metadata",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTrailUpdate(cmd.OutOrStdout(), statusStr, title, description, branch, labelAdd, labelRemove)
		},
	}

	cmd.Flags().StringVar(&statusStr, "status", "", "Update status")
	cmd.Flags().StringVar(&title, "title", "", "Update title")
	cmd.Flags().StringVar(&description, "description", "", "Update description")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch to update trail for (defaults to current)")
	cmd.Flags().StringSliceVar(&labelAdd, "add-label", nil, "Add label(s)")
	cmd.Flags().StringSliceVar(&labelRemove, "remove-label", nil, "Remove label(s)")

	return cmd
}

func runTrailUpdate(w io.Writer, statusStr, title, description, branch string, labelAdd, labelRemove []string) error {
	repo, err := strategy.OpenRepository()
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Determine branch
	if branch == "" {
		branch, err = GetCurrentBranch()
		if err != nil {
			return fmt.Errorf("failed to determine current branch: %w", err)
		}
	}

	store := trail.NewStore(repo)
	metadata, err := store.FindByBranch(branch)
	if err != nil {
		return fmt.Errorf("failed to find trail: %w", err)
	}
	if metadata == nil {
		return fmt.Errorf("no trail found for branch %q", branch)
	}

	// Interactive mode when no flags are provided
	noFlags := statusStr == "" && title == "" && description == "" && labelAdd == nil && labelRemove == nil
	if noFlags {
		// Build status options with current value as default
		// Exclude "done" (set on merge) and "closed" (requires permissions)
		var statusOptions []huh.Option[string]
		for _, s := range trail.ValidStatuses() {
			if s == trail.StatusDone || s == trail.StatusClosed {
				continue
			}
			label := string(s)
			if s == metadata.Status {
				label += " (current)"
			}
			statusOptions = append(statusOptions, huh.NewOption(label, string(s)))
		}
		statusStr = string(metadata.Status)
		title = metadata.Title
		description = metadata.Description

		form := NewAccessibleForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Status").
					Options(statusOptions...).
					Value(&statusStr),
				huh.NewInput().
					Title("Title").
					Value(&title),
				huh.NewText().
					Title("Description").
					Value(&description),
			),
		)
		if formErr := form.Run(); formErr != nil {
			return fmt.Errorf("form cancelled: %w", formErr)
		}
	}

	// Validate status if provided
	if statusStr != "" {
		status := trail.Status(statusStr)
		if !status.IsValid() {
			return fmt.Errorf("invalid status %q: valid values are %s", statusStr, formatValidStatuses())
		}
	}

	err = store.Update(metadata.TrailID, func(m *trail.Metadata) {
		if statusStr != "" {
			m.Status = trail.Status(statusStr)
		}
		if title != "" {
			m.Title = title
		}
		if description != "" {
			m.Description = description
		}
		for _, l := range labelAdd {
			if !containsString(m.Labels, l) {
				m.Labels = append(m.Labels, l)
			}
		}
		for _, l := range labelRemove {
			m.Labels = removeString(m.Labels, l)
		}
	})
	if err != nil {
		return fmt.Errorf("failed to update trail: %w", err)
	}

	fmt.Fprintf(w, "Updated trail for branch %s\n", branch)
	return nil
}

// defaultBaseBranch is the fallback base branch name when it cannot be determined.
const defaultBaseBranch = "main"

// AutoCreateTrail creates a trail automatically for the current branch if one doesn't exist.
// This is called during session start for non-main branches.
// If prompt is non-empty, its first line is used as the trail title instead of the branch name.
func AutoCreateTrail(repo *git.Repository, branchName, baseBranch, prompt string) error {
	store := trail.NewStore(repo)

	existing, err := store.FindByBranch(branchName)
	if err == nil && existing != nil {
		return nil // Trail already exists
	}

	trailID, err := trail.GenerateID()
	if err != nil {
		return fmt.Errorf("failed to generate trail ID: %w", err)
	}

	authorName, _ := checkpoint.GetGitAuthorFromRepo(repo)
	now := time.Now()

	title := titleFromPrompt(prompt)
	if title == "" {
		title = trail.HumanizeBranchName(branchName)
	}

	metadata := &trail.Metadata{
		TrailID:   trailID,
		Branch:    branchName,
		Base:      baseBranch,
		Title:     title,
		Status:    trail.StatusInProgress,
		Author:    authorName,
		Assignees: []string{},
		Labels:    []string{},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.Write(metadata, nil, nil); err != nil {
		return fmt.Errorf("failed to create trail: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[entire] Created trail for branch: %s\n", branchName)
	return nil
}

// titleFromPrompt extracts a trail title from the user's prompt.
// Uses the first line, trimmed and truncated to 80 characters.
// Returns empty string if prompt is empty.
func titleFromPrompt(prompt string) string {
	if prompt == "" {
		return ""
	}
	line, _, _ := strings.Cut(prompt, "\n")
	title := strings.TrimSpace(line)
	if len(title) > 80 {
		title = title[:77] + "..."
	}
	return title
}

// hasFlag is a simple helper that checks os.Args for --flag presence.
// Used to distinguish between "flag not provided" and "flag provided with empty value".
func hasFlag(name string) bool {
	for _, arg := range os.Args {
		if arg == "--"+name || strings.HasPrefix(arg, "--"+name+"=") {
			return true
		}
	}
	return false
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func formatValidStatuses() string {
	statuses := trail.ValidStatuses()
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	var result []string
	for _, v := range slice {
		if v != s {
			result = append(result, v)
		}
	}
	return result
}

// runTrailCreateInteractive runs the interactive form for trail creation.
// Prompts for title, description, branch (derived from title), and status.
func runTrailCreateInteractive(title, description, branch, statusStr *string) error {
	// Step 1: Title and description
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Trail title").
				Placeholder("What are you working on?").
				Value(title),
			huh.NewText().
				Title("Description (optional)").
				Value(description),
		),
	)
	if err := form.Run(); err != nil {
		return fmt.Errorf("form cancelled: %w", err)
	}
	*title = strings.TrimSpace(*title)
	if *title == "" {
		return errors.New("trail title is required")
	}

	// Step 2: Branch (derived from title) and status
	suggested := slugifyTitle(*title)
	*branch = suggested

	// Build status options, excluding done/closed
	var statusOptions []huh.Option[string]
	for _, s := range trail.ValidStatuses() {
		if s == trail.StatusDone || s == trail.StatusClosed {
			continue
		}
		statusOptions = append(statusOptions, huh.NewOption(string(s), string(s)))
	}
	if *statusStr == "" {
		*statusStr = string(trail.StatusDraft)
	}

	form = NewAccessibleForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Branch name").
				Placeholder(suggested).
				Value(branch),
			huh.NewSelect[string]().
				Title("Status").
				Options(statusOptions...).
				Value(statusStr),
		),
	)
	if err := form.Run(); err != nil {
		return fmt.Errorf("form cancelled: %w", err)
	}
	*branch = strings.TrimSpace(*branch)
	if *branch == "" {
		*branch = suggested
	}
	return nil
}

// slugifyTitle converts a title string into a branch-friendly slug.
// Example: "Add user authentication" -> "add-user-authentication"
func slugifyTitle(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	// Replace spaces and underscores with hyphens
	s = strings.NewReplacer(" ", "-", "_", "-").Replace(s)
	// Remove anything that's not alphanumeric, hyphen, or slash
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '/' {
			b.WriteRune(r)
			prevHyphen = false
		} else if r == '-' && !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// branchNeedsCreation checks if a branch exists locally.
func branchNeedsCreation(repo *git.Repository, branchName string) bool {
	_, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	return err != nil
}

// createBranch creates a new local branch pointing at HEAD without checking it out.
func createBranch(repo *git.Repository, branchName string) error {
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}
	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), head.Hash())
	if err := repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("failed to create branch ref: %w", err)
	}
	return nil
}
