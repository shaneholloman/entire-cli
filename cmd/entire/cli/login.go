package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/auth"
	"github.com/spf13/cobra"
)

const fallbackDeviceAuthPollInterval = time.Second
const slowDownBackoff = 5 * time.Second
const maxPollInterval = 30 * time.Second
const maxExpiresIn = 15 * time.Minute
const maxTransientErrors = 5

var browserOpener = openBrowser

// deviceAuthClient abstracts the auth client so runLogin and waitForApproval can be unit-tested.
type deviceAuthClient interface {
	StartDeviceAuth(ctx context.Context) (*auth.DeviceAuthStart, error)
	PollDeviceAuth(ctx context.Context, deviceCode string) (*auth.DeviceAuthPoll, error)
	BaseURL() string
}

func newLoginCmd() *cobra.Command {
	var printBrowserURL bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to Entire",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client := auth.NewClient(nil)
			return runLogin(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), client, printBrowserURL)
		},
	}

	cmd.Flags().BoolVar(&printBrowserURL, "print-browser-url", false, "Print the approval URL instead of opening a browser")

	return cmd
}

func runLogin(ctx context.Context, outW, errW io.Writer, client deviceAuthClient, printBrowserURL bool) error {
	start, err := client.StartDeviceAuth(ctx)
	if err != nil {
		return fmt.Errorf("start login: %w", err)
	}

	fmt.Fprintf(outW, "Device code: %s\n", start.UserCode)
	approvalURL := start.VerificationURIComplete
	if approvalURL == "" {
		approvalURL = start.VerificationURI
	}

	fmt.Fprintf(outW, "Approval URL: %s\n", approvalURL)

	if printBrowserURL {
		fmt.Fprintln(outW, "Open the approval URL in your browser to continue.")
	} else {
		if err := browserOpener(ctx, approvalURL); err != nil {
			fmt.Fprintf(errW, "Warning: failed to open browser automatically: %v\n", err)
			fmt.Fprintln(outW, "Open the approval URL in your browser to continue.")
		} else {
			fmt.Fprintln(outW, "Opened your browser for approval.")
		}
	}

	fmt.Fprintln(outW, "Waiting for approval...")

	token, err := waitForApproval(ctx, client, start.DeviceCode, start.ExpiresIn, start.Interval)
	if err != nil {
		return fmt.Errorf("complete login: %w", err)
	}

	store, err := auth.NewStore()
	if err != nil {
		return fmt.Errorf("create auth store: %w", err)
	}

	if err := store.SaveToken(client.BaseURL(), token); err != nil {
		return fmt.Errorf("save auth token: %w", err)
	}

	fmt.Fprintln(outW, "Login complete.")
	fmt.Fprintf(errW, "Token saved to %s\n", store.FilePath())
	return nil
}

func waitForApproval(ctx context.Context, poller deviceAuthClient, deviceCode string, expiresIn, interval int) (string, error) {
	expiry := time.Duration(expiresIn) * time.Second
	if expiry <= 0 || expiry > maxExpiresIn {
		expiry = maxExpiresIn
	}
	deadline := time.Now().Add(expiry)
	pollInterval := time.Duration(interval) * time.Second
	if pollInterval <= 0 {
		pollInterval = fallbackDeviceAuthPollInterval
	}

	consecutiveErrors := 0

	for {
		if time.Now().After(deadline) {
			return "", errors.New("device authorization expired")
		}

		result, err := poller.PollDeviceAuth(ctx, deviceCode)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= maxTransientErrors {
				return "", fmt.Errorf("poll approval status (after %d consecutive failures): %w", consecutiveErrors, err)
			}
			// Transient error — wait and retry.
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("wait for approval: %w", ctx.Err())
			case <-time.After(pollInterval):
			}
			continue
		}
		consecutiveErrors = 0

		switch result.Error {
		case "", "authorization_pending":
			// continue below
		case "slow_down":
			pollInterval += slowDownBackoff
			if pollInterval > maxPollInterval {
				pollInterval = maxPollInterval
			}
		case "access_denied":
			return "", errors.New("device authorization denied")
		case "expired_token":
			return "", errors.New("device authorization expired")
		default:
			return "", fmt.Errorf("device authorization failed: %s", result.Error)
		}

		if result.Error == "" {
			if result.AccessToken == "" {
				return "", errors.New("device authorization completed without a token")
			}
			return result.AccessToken, nil
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for approval: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func openBrowser(ctx context.Context, browserURL string) error {
	u, err := url.Parse(browserURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("refusing to open non-HTTP URL: %s", browserURL)
	}

	var command string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		command = "open"
		args = []string{browserURL}
	case "linux":
		command = "xdg-open"
		args = []string{browserURL}
	case "windows":
		command = "cmd"
		args = []string{"/c", "start", "", browserURL}
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start browser command %q: %w", command, err)
	}

	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release browser process: %w", err)
	}

	return nil
}
