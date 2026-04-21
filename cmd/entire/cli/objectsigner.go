package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/entireio/cli/cmd/entire/cli/logging"
	"github.com/go-git/go-git/v6/config"
	format "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/x/plugin"
	"github.com/go-git/x/plugin/objectsigner/auto"
	"golang.org/x/crypto/ssh/agent"
)

var registerObjectSignerOnce sync.Once

func RegisterObjectSigner() {
	registerObjectSignerOnce.Do(func() {
		//nolint:errcheck,gosec // best-effort; if registration fails, commits are left unsigned
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

			// When gpg.ssh.program is configured (e.g. 1Password's op-ssh-sign),
			// signing happens via an external binary that go-git cannot invoke.
			// Skip native signing silently — checkpoint commits will be unsigned,
			// which is acceptable since signing is best-effort.
			if auto.Format(merged.GPG.Format) == auto.FormatSSH && hasSSHSignProgram(merged.Raw) {
				logging.Debug(context.Background(), "skipping native SSH commit signing: gpg.ssh.program is configured")
				return nil
			}

			cfg := auto.Config{
				SigningKey: normalizeSigningKey(merged.User.SigningKey, auto.Format(merged.GPG.Format)),
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
	})
}

// connectSSHAgent connects to the SSH agent via SSH_AUTH_SOCK.
// Returns nil if the agent is unavailable.
func connectSSHAgent() agent.Agent { //nolint:ireturn // must return the ssh agent interface
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}

	var d net.Dialer
	conn, err := d.DialContext(context.Background(), "unix", sock)
	if err != nil {
		return nil
	}

	return agent.NewClient(conn)
}

var scopeName = map[config.Scope]string{
	config.GlobalScope: "global",
	config.SystemScope: "system",
}

// hasSSHSignProgram checks whether gpg.ssh.program is set in the raw config.
// go-git's Config struct does not expose this field, so we read it directly.
func hasSSHSignProgram(raw *format.Config) bool {
	if raw == nil {
		return false
	}

	return raw.Section("gpg").Subsection("ssh").Option("program") != ""
}

// sshKeyTypePrefixes are the key type identifiers that can appear at the start
// of an OpenSSH authorized_keys entry. File paths never start with these.
var sshKeyTypePrefixes = []string{"ssh-", "ecdsa-sha2-", "sk-ssh-", "sk-ecdsa-sha2-"}

// normalizeSigningKey prepends "key::" to bare SSH public key literals so that
// the auto signer library routes them through the SSH agent matching path.
// Tools like 1Password set user.signingKey to a bare public key string
// (e.g. "ssh-ed25519 AAAA...") rather than a file path or key:: literal.
func normalizeSigningKey(key string, format auto.Format) string {
	if format != auto.FormatSSH || strings.HasPrefix(key, "key::") {
		return key
	}

	for _, prefix := range sshKeyTypePrefixes {
		if strings.HasPrefix(key, prefix) {
			return "key::" + key
		}
	}

	return key
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
