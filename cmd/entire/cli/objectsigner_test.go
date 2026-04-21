package cli

import (
	"testing"

	format "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/x/plugin/objectsigner/auto"
)

func TestNormalizeSigningKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		key    string
		format auto.Format
		want   string
	}{
		{
			name:   "bare ed25519 public key gets key:: prefix",
			key:    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
			format: auto.FormatSSH,
			want:   "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
		},
		{
			name:   "bare rsa public key gets key:: prefix",
			key:    "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABExample",
			format: auto.FormatSSH,
			want:   "key::ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABExample",
		},
		{
			name:   "bare ecdsa public key gets key:: prefix",
			key:    "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYExample",
			format: auto.FormatSSH,
			want:   "key::ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYExample",
		},
		{
			name:   "bare sk-ssh key gets key:: prefix",
			key:    "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tExample",
			format: auto.FormatSSH,
			want:   "key::sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tExample",
		},
		{
			name:   "bare sk-ecdsa key gets key:: prefix",
			key:    "sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20Example",
			format: auto.FormatSSH,
			want:   "key::sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20Example",
		},
		{
			name:   "already prefixed key:: is unchanged",
			key:    "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
			format: auto.FormatSSH,
			want:   "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
		},
		{
			name:   "file path is unchanged",
			key:    "~/.ssh/id_ed25519",
			format: auto.FormatSSH,
			want:   "~/.ssh/id_ed25519",
		},
		{
			name:   "pub file path is unchanged",
			key:    "~/.ssh/id_ed25519.pub",
			format: auto.FormatSSH,
			want:   "~/.ssh/id_ed25519.pub",
		},
		{
			name:   "absolute file path is unchanged",
			key:    "/home/user/.ssh/id_ed25519",
			format: auto.FormatSSH,
			want:   "/home/user/.ssh/id_ed25519",
		},
		{
			name:   "empty key is unchanged",
			key:    "",
			format: auto.FormatSSH,
			want:   "",
		},
		{
			name:   "openpgp format is unchanged regardless of key",
			key:    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
			format: auto.FormatOpenPGP,
			want:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
		},
		{
			name:   "empty format (defaults to openpgp) is unchanged",
			key:    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
			format: "",
			want:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := normalizeSigningKey(tt.key, tt.format)
			if got != tt.want {
				t.Errorf("normalizeSigningKey(%q, %q) = %q, want %q", tt.key, tt.format, got, tt.want)
			}
		})
	}
}

func TestHasSSHSignProgram(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  *format.Config
		want bool
	}{
		{
			name: "nil raw config",
			raw:  nil,
			want: false,
		},
		{
			name: "empty raw config",
			raw:  format.New(),
			want: false,
		},
		{
			name: "gpg.ssh.program set",
			raw: func() *format.Config {
				c := format.New()
				c.Section("gpg").Subsection("ssh").SetOption("program", "/Applications/1Password.app/Contents/MacOS/op-ssh-sign")
				return c
			}(),
			want: true,
		},
		{
			name: "gpg section exists but no ssh.program",
			raw: func() *format.Config {
				c := format.New()
				c.Section("gpg").SetOption("format", "ssh")
				return c
			}(),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := hasSSHSignProgram(tt.raw)
			if got != tt.want {
				t.Errorf("hasSSHSignProgram() = %v, want %v", got, tt.want)
			}
		})
	}
}
