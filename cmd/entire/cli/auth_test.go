package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/api"
	"github.com/entireio/cli/cmd/entire/cli/auth"
)

const (
	testBaseURL = "https://entire.io"
	testAuthTok = "tok"
	testTokenID = "target-id"
)

// --- status -----------------------------------------------------------------

func TestRunAuthStatus_NotLoggedIn(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()

	listCalled := false
	list := func(context.Context) ([]api.Token, error) {
		listCalled = true
		return nil, nil
	}

	var out bytes.Buffer
	if err := runAuthStatus(context.Background(), &out, store, list, testBaseURL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if listCalled {
		t.Fatal("ListTokens should not be called when no token is stored")
	}
	if !strings.Contains(out.String(), "Not logged in to "+testBaseURL) {
		t.Fatalf("output = %q, want 'Not logged in' message", out.String())
	}
}

func TestRunAuthStatus_LoggedIn(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	list := func(context.Context) ([]api.Token, error) {
		return []api.Token{
			{ID: "a", Name: "laptop"},
			{ID: "b", Name: "ci"},
		}, nil
	}

	var out bytes.Buffer
	if err := runAuthStatus(context.Background(), &out, store, list, testBaseURL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out.String(), "Logged in to "+testBaseURL) {
		t.Fatalf("output = %q, want 'Logged in' message", out.String())
	}
	if !strings.Contains(out.String(), "Active tokens on this account: 2") {
		t.Fatalf("output = %q, want token count", out.String())
	}
}

func TestRunAuthStatus_TokenInvalid(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	list := func(context.Context) ([]api.Token, error) {
		return nil, &api.HTTPError{StatusCode: http.StatusUnauthorized, Message: "Not authenticated"}
	}

	var out bytes.Buffer
	if err := runAuthStatus(context.Background(), &out, store, list, testBaseURL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out.String(), "no longer valid") {
		t.Fatalf("output = %q, want invalid-token message", out.String())
	}
	if !strings.Contains(out.String(), "entire login") {
		t.Fatalf("output = %q, want re-auth hint", out.String())
	}
}

// TestRunAuthStatus_STSRejectionRendersInvalidMessage pins fix #2: in
// split-host setups, STS rejection happens before any HTTP call to the
// data API, so the friendly "Token in keychain ... is no longer valid"
// message has to fire on the auth-go sts package's wrapped string
// (no typed sentinel) as well as the data-API 401 case above.
func TestRunAuthStatus_STSRejectionRendersInvalidMessage(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	list := func(context.Context) ([]api.Token, error) {
		// Exact format auth-go's sts package emits for an invalid_grant
		// 4xx (see internal/oauthhttp's readAPIError). Without the
		// detection in isKeychainTokenRejected this would fall through
		// to the generic "validate token: ..." error path and the user
		// would see a raw STS string instead of the re-login hint.
		return nil, errors.New("token exchange: status 400: invalid_grant: subject_token expired")
	}

	var out bytes.Buffer
	if err := runAuthStatus(context.Background(), &out, store, list, testBaseURL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out.String(), "no longer valid") {
		t.Fatalf("output = %q, want invalid-token message", out.String())
	}
	if !strings.Contains(out.String(), "entire login") {
		t.Fatalf("output = %q, want re-auth hint", out.String())
	}
}

// TestRunAuthStatus_ExpiredCoreTokenRendersInvalidMessage pins the
// other half of fix #2: the tokenmanager's preflight check returns
// auth.ErrNotLoggedIn when a stored core JWT's exp claim is in the
// past. The keyring read at the top of runAuthStatus still finds a
// non-empty entry, so the "Not logged in" branch doesn't fire — the
// helper has to route the wrapped sentinel to the same re-login hint.
func TestRunAuthStatus_ExpiredCoreTokenRendersInvalidMessage(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	list := func(context.Context) ([]api.Token, error) {
		return nil, errors.New("resolve API token: " + auth.ErrNotLoggedIn.Error())
	}
	// errors.New above is intentionally string-only to defeat the
	// detection — confirm the substring fallback alone isn't what's
	// catching this case. The real production path wraps with %w.
	listWithChain := func(context.Context) ([]api.Token, error) {
		return nil, &wrappedTestError{msg: "resolve API token", inner: auth.ErrNotLoggedIn}
	}

	// Sanity: string-only does NOT match (no sentinel chain).
	var out1 bytes.Buffer
	if err := runAuthStatus(context.Background(), &out1, store, list, testBaseURL); err == nil {
		t.Fatal("string-only ErrNotLoggedIn should not match — keep the test honest")
	}

	// Real path: errors.Is sees the sentinel through the %w chain.
	var out2 bytes.Buffer
	if err := runAuthStatus(context.Background(), &out2, store, listWithChain, testBaseURL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out2.String(), "no longer valid") {
		t.Fatalf("output = %q, want invalid-token message", out2.String())
	}
}

// wrappedTestError is a tiny stand-in for fmt.Errorf("...: %w", inner) — kept
// local so the test reads as "this is what production hands runAuthStatus".
type wrappedTestError struct {
	msg   string
	inner error
}

func (e *wrappedTestError) Error() string { return e.msg + ": " + e.inner.Error() }
func (e *wrappedTestError) Unwrap() error { return e.inner }

func TestRunAuthStatus_ServerError(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	list := func(context.Context) ([]api.Token, error) {
		return nil, errors.New("connection refused")
	}

	var out bytes.Buffer
	err := runAuthStatus(context.Background(), &out, store, list, testBaseURL)
	if err == nil {
		t.Fatal("expected error for non-401 failure")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %v, want underlying message", err)
	}
}

// --- list -------------------------------------------------------------------

func TestRunAuthList_NotLoggedInErrors(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()

	var out bytes.Buffer
	err := runAuthList(context.Background(), &out, store,
		func(context.Context) ([]api.Token, error) { return nil, nil },
		testBaseURL, false)
	if err == nil {
		t.Fatal("expected error when not logged in")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("error = %v, want 'not logged in' message", err)
	}
}

func TestRunAuthList_TablePrintsRows(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	lastUsed := "2026-04-01T12:00:00Z"
	list := func(context.Context) ([]api.Token, error) {
		return []api.Token{
			{ID: "tok-1", Name: "laptop", Scope: "cli",
				CreatedAt:  "2026-01-01T00:00:00Z",
				ExpiresAt:  "2027-01-01T00:00:00Z",
				LastUsedAt: &lastUsed},
			{ID: "tok-2", Name: "ci", Scope: "cli",
				CreatedAt:  "2026-02-01T00:00:00Z",
				ExpiresAt:  "2027-01-01T00:00:00Z",
				LastUsedAt: nil},
		}, nil
	}

	var out bytes.Buffer
	if err := runAuthList(context.Background(), &out, store, list, testBaseURL, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "ID") || !strings.Contains(output, "NAME") {
		t.Fatalf("output = %q, want table headers", output)
	}
	if !strings.Contains(output, "tok-1") || !strings.Contains(output, "laptop") {
		t.Fatalf("output = %q, want first row", output)
	}
	if !strings.Contains(output, "tok-2") || !strings.Contains(output, "never") {
		t.Fatalf("output = %q, want second row with 'never' last-used", output)
	}
	// tok-1 last-used recently so should sort before tok-2 in the table.
	if strings.Index(output, "tok-1") > strings.Index(output, "tok-2") {
		t.Fatalf("output = %q, want tok-1 before tok-2 (recent-first)", output)
	}
}

func TestRunAuthList_JSONOutput(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	list := func(context.Context) ([]api.Token, error) {
		return []api.Token{{ID: "tok-1", Name: "laptop"}}, nil
	}

	var out bytes.Buffer
	if err := runAuthList(context.Background(), &out, store, list, testBaseURL, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := out.String()
	if !strings.HasPrefix(strings.TrimSpace(output), "[") {
		t.Fatalf("output = %q, want JSON array", output)
	}
	if !strings.Contains(output, `"id": "tok-1"`) {
		t.Fatalf("output = %q, want decoded id", output)
	}
}

func TestRunAuthList_EmptyPrintsMessage(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	list := func(context.Context) ([]api.Token, error) { return nil, nil }

	var out bytes.Buffer
	if err := runAuthList(context.Background(), &out, store, list, testBaseURL, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "No active tokens") {
		t.Fatalf("output = %q, want 'No active tokens' message", out.String())
	}
}

func TestFormatAuthLastUsed_RelativeBuckets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		input *string
		want  string
	}{
		"nil": {nil, "never"},
		"just now": {
			ptr(now.Add(-30 * time.Second).Format(time.RFC3339)),
			"just now",
		},
		"minutes ago": {
			ptr(now.Add(-15 * time.Minute).Format(time.RFC3339)),
			"15m ago",
		},
		"hours ago": {
			ptr(now.Add(-3 * time.Hour).Format(time.RFC3339)),
			"3h ago",
		},
		"yesterday": {
			ptr(now.Add(-30 * time.Hour).Format(time.RFC3339)),
			"yesterday",
		},
		"days ago": {
			ptr(now.Add(-5 * 24 * time.Hour).Format(time.RFC3339)),
			"5d ago",
		},
		"old absolute": {
			ptr(now.Add(-90 * 24 * time.Hour).Format(time.RFC3339)),
			now.Add(-90 * 24 * time.Hour).Local().Format("2006-01-02"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := formatAuthLastUsed(tt.input, now); got != tt.want {
				t.Errorf("formatAuthLastUsed(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestClassifyExpiresAt_Buckets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		input string
		want  expiresState
	}{
		"empty":   {"", expiresNormal},
		"expired": {now.Add(-time.Hour).Format(time.RFC3339), expiresExpired},
		"soon":    {now.Add(3 * 24 * time.Hour).Format(time.RFC3339), expiresSoon},
		"normal":  {now.Add(60 * 24 * time.Hour).Format(time.RFC3339), expiresNormal},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := classifyExpiresAt(tt.input, now); got != tt.want {
				t.Errorf("classifyExpiresAt(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// --- revoke -----------------------------------------------------------------

func TestRunAuthRevoke_ByIDCallsRevoker(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	var gotID string
	revokeByID := func(_ context.Context, id string) error {
		gotID = id
		return nil
	}

	revokeCurrentCalled := false
	revokeCurrent := func(context.Context) error {
		revokeCurrentCalled = true
		return nil
	}

	// list returns 200 → token id was someone else's, no local cleanup expected.
	list := func(context.Context) ([]api.Token, error) {
		return []api.Token{{ID: "other"}}, nil
	}

	var out, errOut bytes.Buffer
	err := runAuthRevoke(context.Background(), &out, &errOut, store,
		list, revokeByID, revokeCurrent, testBaseURL, testTokenID, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if revokeCurrentCalled {
		t.Fatal("revokeCurrent should not be called when revoking by id")
	}
	if gotID != testTokenID {
		t.Errorf("revokeByID called with id=%q, want %q", gotID, testTokenID)
	}
	if store.deleted[testBaseURL] {
		t.Fatal("local token should NOT be deleted when revoking another token")
	}
	if !strings.Contains(out.String(), "Revoked token "+testTokenID) {
		t.Fatalf("output = %q, want confirmation", out.String())
	}
	if strings.Contains(out.String(), "removed from keychain") {
		t.Fatalf("output = %q, should not mention keychain cleanup for non-self revoke", out.String())
	}
}

func TestRunAuthRevoke_ByIDSelfRevokeCleansLocal(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	revokeByID := func(context.Context, string) error { return nil }
	revokeCurrent := func(context.Context) error { return nil }

	// list returns 401 → the id we just revoked was our own bearer.
	list := func(context.Context) ([]api.Token, error) {
		return nil, &api.HTTPError{StatusCode: http.StatusUnauthorized, Message: "Not authenticated"}
	}

	var out, errOut bytes.Buffer
	err := runAuthRevoke(context.Background(), &out, &errOut, store,
		list, revokeByID, revokeCurrent, testBaseURL, testTokenID, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !store.deleted[testBaseURL] {
		t.Fatal("local token should be deleted after self-revoke")
	}
	if !strings.Contains(out.String(), "removed from keychain") {
		t.Fatalf("output = %q, want self-revoke confirmation message", out.String())
	}
}

func TestRunAuthRevoke_CurrentDelegatesToLogout(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()
	store.tokens[testBaseURL] = testAuthTok

	revokeByIDCalled := false
	revokeByID := func(context.Context, string) error {
		revokeByIDCalled = true
		return nil
	}

	revokeCurrentCalled := false
	revokeCurrent := func(context.Context) error {
		revokeCurrentCalled = true
		return nil
	}

	list := func(context.Context) ([]api.Token, error) { return nil, nil }

	var out, errOut bytes.Buffer
	err := runAuthRevoke(context.Background(), &out, &errOut, store,
		list, revokeByID, revokeCurrent, testBaseURL, "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if revokeByIDCalled {
		t.Fatal("revokeByID should not be called when --current is set")
	}
	if !revokeCurrentCalled {
		t.Fatal("revokeCurrent should be called when --current is set")
	}
	if !store.deleted[testBaseURL] {
		t.Fatal("local token should be deleted via logout path")
	}
	if !strings.Contains(out.String(), "Logged out.") {
		t.Fatalf("output = %q, want 'Logged out.' message from logout path", out.String())
	}
}

func TestRunAuthRevoke_NotLoggedInErrors(t *testing.T) {
	t.Parallel()

	store := newMockTokenStore()

	var out, errOut bytes.Buffer
	err := runAuthRevoke(context.Background(), &out, &errOut, store,
		func(context.Context) ([]api.Token, error) { return nil, nil },
		func(context.Context, string) error { return nil },
		func(context.Context) error { return nil },
		testBaseURL, "some-id", false)
	if err == nil {
		t.Fatal("expected error when not logged in")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("error = %v, want 'not logged in' message", err)
	}
}

// --- registration -----------------------------------------------------------

func TestAuthCmd_RegistersExpectedSubcommands(t *testing.T) {
	t.Parallel()

	root := NewRootCmd()
	var authCmd *struct{}
	for _, c := range root.Commands() {
		if c.Use == "auth" {
			authCmd = &struct{}{}
			subcommands := map[string]bool{}
			for _, sub := range c.Commands() {
				name := strings.Fields(sub.Use)[0]
				subcommands[name] = true
			}
			for _, want := range []string{"login", "logout", "status", "list", "revoke"} {
				if !subcommands[want] {
					t.Errorf("auth missing subcommand %q (got: %v)", want, subcommands)
				}
			}
		}
	}
	if authCmd == nil {
		t.Fatal("auth command not registered on root")
	}
}

// --- isKeychainTokenRejected -----------------------------------------------

func TestIsKeychainTokenRejected_AllShapes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want bool
	}{
		"data API 401":           {&api.HTTPError{StatusCode: http.StatusUnauthorized}, true},
		"data API 500":           {&api.HTTPError{StatusCode: http.StatusInternalServerError}, false},
		"ErrNotLoggedIn":         {auth.ErrNotLoggedIn, true},
		"wrapped ErrNotLoggedIn": {errors.New("resolve API token: " + auth.ErrNotLoggedIn.Error()), false /* string-only, no chain — not detected */},
		"sts 401":                {errors.New("token exchange: status 401: invalid_client"), true},
		"sts 400 invalid_grant":  {errors.New("token exchange: status 400: invalid_grant: token expired"), true},
		"sts 500":                {errors.New("token exchange: status 500: server_error"), false},
		"network error":          {errors.New("dial tcp: i/o timeout"), false},
	}

	// Confirm wrapped chains do propagate (the "wrapped ErrNotLoggedIn"
	// case above uses string substitution which intentionally doesn't
	// preserve the sentinel; this case uses fmt.Errorf %w which does).
	cases["fmt.Errorf %w ErrNotLoggedIn"] = struct {
		err  error
		want bool
	}{errors.Join(errors.New("resolve API token"), auth.ErrNotLoggedIn), true}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := isKeychainTokenRejected(tc.err); got != tc.want {
				t.Errorf("isKeychainTokenRejected(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestAuthCmd_TopLevelLoginAndLogoutStillRegistered(t *testing.T) {
	t.Parallel()

	root := NewRootCmd()
	want := map[string]bool{"login": false, "logout": false}
	for _, c := range root.Commands() {
		if _, ok := want[c.Use]; ok {
			want[c.Use] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("top-level %q command should remain registered", name)
		}
	}
}
