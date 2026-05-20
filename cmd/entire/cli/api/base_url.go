package api

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ErrInsecureHTTP is returned when the base URL uses HTTP without an explicit opt-in.
var ErrInsecureHTTP = errors.New("refusing to use insecure http:// base URL for authentication (use --insecure-http-auth to override)")

const (
	// DefaultBaseURL is the production Entire API origin.
	DefaultBaseURL = "https://entire.io"

	// BaseURLEnvVar overrides the Entire API origin for local development.
	BaseURLEnvVar = "ENTIRE_API_BASE_URL"

	// AuthBaseURLEnvVar overrides only the auth/login origin (device flow,
	// auth-tokens management, keyring key). Falls back to BaseURLEnvVar when
	// unset, which is the right behavior for single-host deployments. Split
	// hosts (e.g. auth on us.console.partial.to, data on partial.to) set
	// both.
	AuthBaseURLEnvVar = "ENTIRE_AUTH_BASE_URL"
)

// BaseURL returns the effective Entire API base URL.
// ENTIRE_API_BASE_URL takes precedence over the production default.
func BaseURL() string {
	if raw := strings.TrimSpace(os.Getenv(BaseURLEnvVar)); raw != "" {
		return normalizeBaseURL(raw)
	}

	return DefaultBaseURL
}

// AuthBaseURL returns the origin used for the device-flow login, auth-token
// management endpoints, and the keyring key under which the bearer token is
// stored. ENTIRE_AUTH_BASE_URL takes precedence; otherwise it falls back to
// BaseURL() so single-host deployments keep working unchanged.
func AuthBaseURL() string {
	if raw := strings.TrimSpace(os.Getenv(AuthBaseURLEnvVar)); raw != "" {
		return normalizeBaseURL(raw)
	}

	return BaseURL()
}

// ResolveURL joins an API-relative path against the effective base URL.
func ResolveURL(path string) (string, error) {
	return ResolveURLFromBase(BaseURL(), path)
}

// ResolveURLFromBase joins an API-relative path against an explicit base URL.
// Only http and https schemes are accepted.
func ResolveURLFromBase(baseURL, path string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}

	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("unsupported base URL scheme %q (must be http or https)", base.Scheme)
	}

	rel, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse path: %w", err)
	}

	return base.ResolveReference(rel).String(), nil
}

// RequireSecureURL returns ErrInsecureHTTP if the base URL uses the http scheme.
// Call this before making authenticated requests unless --insecure-http-auth is set.
func RequireSecureURL(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}

	if u.Scheme == "http" {
		return ErrInsecureHTTP
	}

	return nil
}

func normalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// OriginOnly returns the scheme+host of raw, stripping any path/query/fragment
// and userinfo. Falls through verbatim when raw doesn't parse as an absolute
// URL — callers that need strict validation should check the return value
// against the input or use url.Parse themselves. Used both to feed origin-only
// URLs into tokenmanager.TokenRequest.Resource (which validates them) and to
// compare JWT iss claims against expected issuers.
func OriginOnly(raw string) string {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return trimmed
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}
