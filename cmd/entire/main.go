package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/entireio/cli/cmd/entire/cli"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/x/plugin"
	"github.com/go-git/x/plugin/objectsigner/auto"
	"github.com/spf13/cobra"

	"golang.org/x/crypto/ssh/agent"
)

func main() {
	registerObjectSigner()

	// Create context that cancels on interrupt
	ctx, cancel := context.WithCancel(context.Background())

	// Handle interrupt signals
	sigChan := make(chan os.Signal, 1)
	signals := []os.Signal{os.Interrupt}
	if runtime.GOOS != "windows" {
		signals = append(signals, syscall.SIGTERM)
	}
	signal.Notify(sigChan, signals...)
	go func() {
		<-sigChan
		cancel()
	}()

	// Create and execute root command
	rootCmd := cli.NewRootCmd()
	err := rootCmd.ExecuteContext(ctx)
	if err != nil {
		var silent *cli.SilentError

		switch {
		case errors.As(err, &silent):
			// Command already printed the error
		case strings.Contains(err.Error(), "unknown command") || strings.Contains(err.Error(), "unknown flag"):
			showSuggestion(rootCmd, err)
		default:
			fmt.Fprintln(rootCmd.OutOrStderr(), err)
		}

		cancel()
		os.Exit(1)
	}
	cancel() // Cleanup on successful exit
}

func registerObjectSigner() {
	plugin.Register(plugin.ObjectSigner(), func() plugin.Signer {
		cfgSource, err := plugin.Get(plugin.ConfigLoader())
		if err != nil {
			// No config loader registered; signing not possible.
			return nil
		}

		sysCfg := loadScopedConfig(cfgSource, config.SystemScope)
		globalCfg := loadScopedConfig(cfgSource, config.GlobalScope)

		// Merge system then global so that global settings take precedence.
		merged := config.Merge(sysCfg, globalCfg)

		if !merged.Commit.GpgSign.IsTrue() {
			return nil
		}

		cfg := auto.Config{
			SigningKey: merged.User.SigningKey,
			Format:     auto.Format(merged.GPG.Format),
			SSHAgent:   connectSSHAgent(),
		}

		signer, err := auto.FromConfig(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to create object signer: %v\n", err)
			return nil
		}

		return signer
	})
}

// connectSSHAgent connects to the SSH agent via SSH_AUTH_SOCK.
// Returns nil if the agent is unavailable.
func connectSSHAgent() agent.Agent {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}

	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}

	return agent.NewClient(conn)
}

var scopeName = map[config.Scope]string{
	config.GlobalScope: "global",
	config.SystemScope: "system",
}

func loadScopedConfig(source plugin.ConfigSource, scope config.Scope) *config.Config {
	name := scopeName[scope]

	storer, err := source.Load(scope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to load %s git config: %v\n", name, err)
		return config.NewConfig()
	}

	cfg, err := storer.Config()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to parse %s git config: %v\n", name, err)
		return config.NewConfig()
	}

	return cfg
}

func showSuggestion(cmd *cobra.Command, err error) {
	// Print usage first (brew style)
	fmt.Fprint(cmd.OutOrStderr(), cmd.UsageString())
	fmt.Fprintf(cmd.OutOrStderr(), "\nError: Invalid usage: %v\n", err)
}
