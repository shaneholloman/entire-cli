package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/entireio/cli/internal/coreapi"
)

// mirrorColumns is the human table/field view of a mirror: the scannable
// repo name, the clone URL you'd copy, and whether the upstream is
// private. The cluster is omitted — it's already embedded in the clone
// URL — and the wire model's internal ids are dropped entirely. The clone
// URL is synthesised from the mirror's coords (the form `git clone`
// accepts), since the list API doesn't return it.
var mirrorColumns = []string{"REPO", "CLONE URL", "PRIVATE"}

func mirrorRow(m coreapi.Mirror) []string {
	repo := m.Owner + "/" + m.Repo
	cloneURL := fmt.Sprintf("entire://%s/gh/%s/%s", m.ClusterHost, m.Owner, m.Repo)
	private := "no"
	if m.IsPrivate.Or(false) {
		private = "yes"
	}
	return []string{repo, cloneURL, private}
}

// newRepoMirrorCmd is the `entire repo mirror` subtree: manage EntireDB
// GitHub-mirror placements on a cluster. Mirrors the standalone entiredb
// CLI's `entire repo mirror` surface for the server-side half (create /
// list / get / remove). The local-clone rewrite (`mirror use`) is not
// ported — it's a git-config + git-remote-entire concern outside the
// control-plane API.
func newRepoMirrorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mirror",
		Short: "Manage GitHub-mirror placements on EntireDB clusters",
	}
	cmd.AddCommand(newRepoMirrorCreateCmd())
	cmd.AddCommand(newRepoMirrorListCmd())
	cmd.AddCommand(newRepoMirrorGetCmd())
	cmd.AddCommand(newRepoMirrorRemoveCmd())
	return cmd
}

func newRepoMirrorCreateCmd() *cobra.Command {
	var (
		noWait      bool
		waitTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "create <github-url> <cluster-host>",
		Short: "Register a GitHub mirror on a cluster",
		Long: "Registers a mirror placement for a GitHub repo on the named " +
			"cluster, then waits for the initial GitHub→EntireDB clone to " +
			"finish so `git clone` works on return. Pass --no-wait to return " +
			"as soon as the placement is registered. Idempotent on " +
			"(upstream, cluster).",
		Example: "  entire repo mirror create github.com/octocat/hello-world aws-us-east-2.entire.io",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, repo, err := parseGitHubURL(args[0])
			if err != nil {
				cmd.SilenceUsage = true
				return NewSilentError(fmt.Errorf("invalid <github-url>: %w", err))
			}
			clusterHost := args[1]
			return runCore(cmd, func(ctx context.Context, c *coreapi.Client) error {
				created, err := c.CreateMirror(ctx, &coreapi.CreateMirrorInputBody{
					Provider:    coreapi.CreateMirrorInputBodyProviderGithub,
					Owner:       owner,
					Repo:        repo,
					ClusterHost: clusterHost,
				})
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				if created.Created {
					fmt.Fprintf(out, "Registered mirror %s\n", created.MirrorId)
				} else {
					fmt.Fprintf(out, "Mirror already exists (%s)\n", created.MirrorId)
				}
				fmt.Fprintf(out, "  %s\n", created.MirrorUrl)
				if noWait {
					fmt.Fprintf(out, "Initial clone may still be in progress; `git clone %s` will work once it completes.\n", created.MirrorUrl)
					return nil
				}
				if err := waitForMirrorClone(ctx, out, clusterHost, owner, repo, waitTimeout); err != nil {
					return err
				}
				fmt.Fprintf(out, "\nClone it:\n  git clone %s\n", created.MirrorUrl)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the placement is registered, without waiting for the initial clone")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 30*time.Minute, "how long to wait for the initial clone to finish")
	return cmd
}

func newRepoMirrorListCmd() *cobra.Command {
	var cluster, provider, owner string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List mirrors you can see",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCoreList(cmd, mirrorColumns, mirrorRow, func(ctx context.Context, c *coreapi.Client) ([]coreapi.Mirror, error) {
				var params coreapi.ListMirrorsParams
				if cluster != "" {
					params.Cluster = coreapi.NewOptString(cluster)
				}
				if provider != "" {
					params.Provider = coreapi.NewOptString(provider)
				}
				if owner != "" {
					params.Owner = coreapi.NewOptString(owner)
				}
				out, err := c.ListMirrors(ctx, params)
				if err != nil {
					return nil, err
				}
				return out.Mirrors, nil
			})
		},
	}
	cmd.Flags().StringVar(&cluster, "cluster", "", "filter by cluster public host")
	cmd.Flags().StringVar(&provider, "provider", "", "filter by upstream provider (e.g. github)")
	cmd.Flags().StringVar(&owner, "owner", "", "filter by upstream owner login")
	return cmd
}

func newRepoMirrorGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <mirror-id>",
		Short: "Show a mirror by ULID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoreObject(cmd, mirrorColumns, mirrorRow, func(ctx context.Context, c *coreapi.Client) (*coreapi.Mirror, error) {
				return c.GetMirror(ctx, coreapi.GetMirrorParams{MirrorId: args[0]})
			})
		},
	}
}

func newRepoMirrorRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <github-url> <cluster-host>",
		Short: "Un-register a GitHub mirror from a cluster",
		Long: "Removes a mirror placement for a GitHub repo from the named " +
			"cluster. Other clusters' placements of the same upstream are " +
			"unaffected.",
		Example: "  entire repo mirror remove github.com/octocat/hello-world aws-us-east-2.entire.io",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, repo, err := parseGitHubURL(args[0])
			if err != nil {
				cmd.SilenceUsage = true
				return NewSilentError(fmt.Errorf("invalid <github-url>: %w", err))
			}
			clusterHost := args[1]
			return runCore(cmd, func(ctx context.Context, c *coreapi.Client) error {
				// The mirror's data-plane ULID is resolved from its GitHub
				// coords; DeleteMirror then takes that id.
				resolved, err := c.LookupRepoByMirror(ctx, coreapi.LookupRepoByMirrorParams{
					Provider:    "github",
					Owner:       owner,
					Repo:        repo,
					ClusterHost: clusterHost,
				})
				if err != nil {
					return err
				}
				if err := c.DeleteMirror(ctx, coreapi.DeleteMirrorParams{MirrorId: resolved.RepoId}); err != nil {
					return err
				}
				cmd.Printf("Removed mirror github.com/%s/%s from %s\n", owner, repo, clusterHost)
				return nil
			})
		},
	}
}
