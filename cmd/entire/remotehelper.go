package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/auth"
	"github.com/entireio/cli/cmd/entire/cli/versioninfo"
	"github.com/entireio/cli/internal/entireclient/clusterdiscovery"
	"github.com/entireio/cli/internal/entireclient/contexts"
	"github.com/entireio/cli/internal/entireclient/httpclient"
	"github.com/entireio/cli/internal/entireclient/repocreds"
	"github.com/entireio/cli/internal/remotehelper/debuglog"
	"github.com/entireio/cli/internal/remotehelper/githelper"
	"github.com/entireio/cli/internal/remotehelper/replicas"
	"github.com/entireio/cli/internal/remotehelper/transport"
)

// remoteHelperName is the git remote-helper binary name. Git resolves
// `entire://` URLs by exec'ing a binary called git-remote-entire found on
// PATH; we ship that as a symlink to the entire CLI and dispatch on
// argv[0] so a single binary serves both roles (busybox/git style).
const remoteHelperName = "git-remote-entire"

// invokedAsRemoteHelper reports whether this process was launched under
// the git-remote-entire name (via the shipped symlink). Detection is by
// argv[0] basename, with a Windows .exe suffix tolerated.
func invokedAsRemoteHelper(arg0 string) bool {
	base := strings.TrimSuffix(filepath.Base(arg0), ".exe")
	return base == remoteHelperName
}

// runRemoteHelper implements the git remote-helper protocol for
// `entire://` URLs. Git invokes us as `git-remote-entire <remote> <url>`,
// hands us the helper protocol on stdin, and expects responses on stdout.
//
// IMPORTANT: nothing on this path may write to stdout except the helper
// protocol itself — git parses stdout as a strict pkt-line stream, so a
// stray banner or log line corrupts the transfer. Diagnostics go to
// stderr (and the ENTIRE_DEBUG-gated debuglog).
//
// Authentication resolves the login context for the target cluster from
// the shared contexts.json (honoring an explicit cluster binding, else
// /.well-known discovery), then mints repo-scoped tokens by exchanging
// that context's login JWT. A pre-contexts.json login is migrated in
// read-time so existing users don't have to re-authenticate.
func runRemoteHelper(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: git-remote-entire <remote-name> <url>")
		return 128
	}

	// Build info drives the agent string the helper advertises upstream.
	versioninfo.Load()
	githelper.Agent = remoteHelperName + "/" + versioninfo.Commit

	rawURL := args[2]
	parsedURL, err := url.Parse(rawURL)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "fatal: invalid URL %q: %v\n", rawURL, err)
		return 128
	case parsedURL.Scheme != "entire":
		fmt.Fprintf(os.Stderr, "fatal: unsupported URL scheme %q (expected 'entire')\n", parsedURL.Scheme)
		return 128
	case parsedURL.Host == "":
		fmt.Fprintf(os.Stderr, "fatal: missing host in URL %q\n", rawURL)
		return 128
	}

	ctx, stop := installRemoteHelperSignals()
	defer stop()

	skipTLS := os.Getenv("ENTIRE_TLS_SKIP_VERIFY") == "true"

	nodeCfg := replicas.Resolve(parsedURL)
	// The repo-scoped token's audience is <clusterBaseURL><repoSlug>. The
	// audience pins to the cluster entry URL (not a replica node), matching
	// what the server validates the exchanged token against.
	clusterBaseURL := nodeCfg.EntryURL
	repoSlug := parsedURL.Path

	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: httpclient.NewTransport(skipTLS),
	}

	// Bridge any pre-contexts.json login so the resolver can find it.
	if _, err := auth.MigrateLegacyLoginContext(); err != nil {
		debuglog.Printf("legacy login migration: %v", err)
	}

	// Resolve which login context authenticates this cluster: an explicit
	// cluster_contexts binding wins, otherwise the cluster's
	// /.well-known/entire-cluster.json is matched against local contexts.
	cfgDir := contexts.DefaultConfigDir()
	clusterCtx, err := clusterdiscovery.ResolveContextForCluster(ctx, cfgDir, parsedURL.Host, httpClient, debuglog.Printf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		return 128
	}

	// Mint repo-scoped tokens by exchanging the context's login JWT at its
	// core's /oauth/token, cached per (repo, action) for this invocation.
	creds := repocreds.New(clusterCtx.CoreURL, clusterBaseURL, func(context.Context) (string, error) {
		return auth.LoginTokenForContext(clusterCtx)
	}, httpClient)

	setAuth := func(req *http.Request) error {
		action := gitActionFromRequest(req)
		if action == "" {
			return fmt.Errorf("cannot classify git op for %s %s; scoped-token exchange requires a recognised smart-HTTP endpoint", req.Method, req.URL.Path)
		}
		token, err := creds.Token(req.Context(), repoSlug, action)
		if err != nil {
			return fmt.Errorf("repo-scoped token exchange: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}

	var onNodeFailed func(string)
	if nodeCfg.Caching() {
		onNodeFailed = func(string) { replicas.Invalidate(nodeCfg.ClusterHost, nodeCfg.RepoPath) }
	}

	proxy := transport.New(transport.Config{
		Nodes:        nodeCfg,
		Path:         parsedURL.Path,
		SkipTLS:      skipTLS,
		SetAuth:      setAuth,
		OnNodeFailed: onNodeFailed,
	})

	mode := githelper.ModeConnect
	if os.Getenv("ENTIRE_STATELESS") == "true" {
		mode = githelper.ModeStateless
	}

	if err := githelper.Run(ctx, proxy, mode, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		return 128
	}
	return 0
}

// gitActionFromRequest classifies a smart-HTTP request as "pull" or
// "push" so the right repo-scoped token can be minted. Returns "" when
// the endpoint isn't a recognised git smart-HTTP route.
func gitActionFromRequest(req *http.Request) string {
	path := req.URL.Path
	switch req.Method {
	case http.MethodPost:
		switch {
		case strings.HasSuffix(path, "/git-receive-pack"):
			return "push"
		case strings.HasSuffix(path, "/git-upload-pack"):
			return "pull"
		}
	case http.MethodGet:
		if strings.HasSuffix(path, "/info/refs") {
			switch req.URL.Query().Get("service") {
			case "git-receive-pack":
				return "push"
			case "git-upload-pack":
				return "pull"
			}
		}
	}
	return ""
}

// installRemoteHelperSignals ties HTTP request lifetimes to the parent
// git process. Ctrl-C delivers SIGINT to the whole foreground process
// group (us included); cancelling ctx aborts in-flight transfers instead
// of waiting out the read timeout. After the first signal we unhook so a
// second Ctrl-C hits the runtime default and hard-exits.
func installRemoteHelperSignals() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
		time.Sleep(2 * time.Second)
		fmt.Fprintln(os.Stderr, "git-remote-entire: shutdown taking longer than expected; press Ctrl-C again to force-quit")
	}()
	return ctx, stop
}
