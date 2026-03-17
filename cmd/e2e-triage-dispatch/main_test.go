package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/internal/slacktriage"
)

const testSigningSecret = "test-signing-secret"

func TestLoadConfigFromEnv_LoadsAllowedRepo(t *testing.T) {
	os.Clearenv()
	t.Setenv("SLACK_SIGNING_SECRET", "secret")
	t.Setenv("SLACK_BOT_TOKEN", "bot")
	t.Setenv("GITHUB_TOKEN", "gh")
	t.Setenv("ALLOWED_REPOSITORY", "entireio/cli")

	cfg, err := loadConfigFromEnv()
	if err != nil {
		t.Fatalf("loadConfigFromEnv() error = %v", err)
	}
	if cfg.AllowedRepo != "entireio/cli" {
		t.Fatalf("AllowedRepo = %q, want %q", cfg.AllowedRepo, "entireio/cli")
	}
}

func TestLoadConfigFromEnv_RequiresAllowedRepo(t *testing.T) {
	os.Clearenv()
	t.Setenv("SLACK_SIGNING_SECRET", "secret")
	t.Setenv("SLACK_BOT_TOKEN", "bot")
	t.Setenv("GITHUB_TOKEN", "gh")

	if _, err := loadConfigFromEnv(); err == nil {
		t.Fatal("loadConfigFromEnv() error = nil, want error")
	}
}

func TestHandler_URLVerification(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	body := `{"type":"url_verification","challenge":"abc123"}`
	req := signedRequest(t, http.MethodPost, "/slack/events", body, testSigningSecret, fixedNow())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var got struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Challenge != "abc123" {
		t.Fatalf("challenge = %q, want %q", got.Challenge, "abc123")
	}
}

func TestHandler_RejectsBadSignature(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(`{"type":"event_callback"}`))
	req.Header.Set("X-Slack-Request-Timestamp", fmt.Sprintf("%d", fixedNow().Unix()))
	req.Header.Set("X-Slack-Signature", "v0=deadbeef")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandler_IgnoresNonThreadReplies(t *testing.T) {
	t.Parallel()

	fetcher := &fakeSlackFetcher{}
	dispatcher := &fakeGitHubDispatcher{}
	handler := newHandlerForTest(t, fetcher, dispatcher)

	body := `{"type":"event_callback","event":{"type":"message","channel":"C123","user":"U123","text":"triage e2e","ts":"111.222","thread_ts":"111.222"}}`
	req := signedRequest(t, http.MethodPost, "/slack/events", body, testSigningSecret, fixedNow())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if fetcher.calls != 0 {
		t.Fatalf("fetch calls = %d, want 0", fetcher.calls)
	}
	if dispatcher.calls != 0 {
		t.Fatalf("dispatch calls = %d, want 0", dispatcher.calls)
	}
}

func TestHandler_IgnoresNonTriggerReplies(t *testing.T) {
	t.Parallel()

	fetcher := &fakeSlackFetcher{}
	dispatcher := &fakeGitHubDispatcher{}
	handler := newHandlerForTest(t, fetcher, dispatcher)

	body := `{"type":"event_callback","event":{"type":"message","channel":"C123","user":"U123","text":"hello world","ts":"111.222","thread_ts":"111.111"}}`
	req := signedRequest(t, http.MethodPost, "/slack/events", body, testSigningSecret, fixedNow())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if fetcher.calls != 0 {
		t.Fatalf("fetch calls = %d, want 0", fetcher.calls)
	}
	if dispatcher.calls != 0 {
		t.Fatalf("dispatch calls = %d, want 0", dispatcher.calls)
	}
}

func TestHandler_DispatchesValidTriggerReply(t *testing.T) {
	t.Parallel()

	fetcher := &fakeSlackFetcher{
		body: "E2E Tests Failed\nmeta: repo=entireio/cli branch=main run_id=123 run_url=https://github.com/entireio/cli/actions/runs/123 sha=abc123 agents=cursor-cli,copilot-cli",
	}
	dispatcher := &fakeGitHubDispatcher{}
	handler := newHandlerForTest(t, fetcher, dispatcher)

	body := `{"type":"event_callback","event":{"type":"message","channel":"C123","user":"U123","text":"triage e2e","ts":"111.222","thread_ts":"111.111"}}`
	req := signedRequest(t, http.MethodPost, "/slack/events", body, testSigningSecret, fixedNow())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if fetcher.calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls)
	}
	if dispatcher.calls != 1 {
		t.Fatalf("dispatch calls = %d, want 1", dispatcher.calls)
	}

	if fetcher.channel != "C123" || fetcher.threadTS != "111.111" {
		t.Fatalf("fetch args = (%q, %q), want (%q, %q)", fetcher.channel, fetcher.threadTS, "C123", "111.111")
	}

	got := dispatcher.payloads[0]
	if got.TriggerText != slacktriage.TriageTriggerText {
		t.Fatalf("trigger = %q, want %q", got.TriggerText, slacktriage.TriageTriggerText)
	}
	if got.Repo != "entireio/cli" || got.Branch != "main" || got.RunID != "123" || got.RunURL != "https://github.com/entireio/cli/actions/runs/123" || got.SHA != "abc123" {
		t.Fatalf("unexpected payload metadata: %+v", got)
	}
	if got.SlackChannel != "C123" || got.SlackThreadTS != "111.111" || got.SlackUser != "U123" {
		t.Fatalf("unexpected slack metadata: %+v", got)
	}
}

func TestHandler_IgnoresBotAndSystemMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "subtype",
			body: `{"type":"event_callback","event":{"type":"message","subtype":"bot_message","channel":"C123","user":"U123","text":"triage e2e","ts":"111.222","thread_ts":"111.111"}}`,
		},
		{
			name: "bot_id",
			body: `{"type":"event_callback","event":{"type":"message","bot_id":"B123","channel":"C123","user":"U123","text":"triage e2e","ts":"111.222","thread_ts":"111.111"}}`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fetcher := &fakeSlackFetcher{}
			dispatcher := &fakeGitHubDispatcher{}
			handler := newHandlerForTest(t, fetcher, dispatcher)

			req := signedRequest(t, http.MethodPost, "/slack/events", tt.body, testSigningSecret, fixedNow())
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if fetcher.calls != 0 {
				t.Fatalf("fetch calls = %d, want 0", fetcher.calls)
			}
			if dispatcher.calls != 0 {
				t.Fatalf("dispatch calls = %d, want 0", dispatcher.calls)
			}
		})
	}
}

func TestGitHubDispatcher_UsesConfiguredRepository(t *testing.T) {
	t.Parallel()

	var gotPath string
	dispatcher := newGitHubHTTPDispatcher("token", "https://api.github.com", defaultSlackEventType, "entireio/cli")
	dispatcher.client = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}, nil
		}),
	}
	payload := slacktriage.DispatchPayload{
		Repo:         "other/repo",
		TriggerText:  slacktriage.TriageTriggerText,
		Branch:       "main",
		SHA:          "abc123",
		RunURL:       "https://github.com/entireio/cli/actions/runs/123",
		RunID:        "123",
		FailedAgents: []string{"cursor-cli"},
	}

	if err := dispatcher.DispatchRepositoryEvent(context.Background(), payload); err != nil {
		t.Fatalf("DispatchRepositoryEvent() error = %v", err)
	}
	if gotPath != "/repos/entireio/cli/dispatches" {
		t.Fatalf("request path = %q, want %q", gotPath, "/repos/entireio/cli/dispatches")
	}
}

func TestHandler_RejectsMismatchedParentRepo(t *testing.T) {
	t.Parallel()

	fetcher := &fakeSlackFetcher{
		body: "E2E Tests Failed\nmeta: repo=other/repo branch=main run_id=123 run_url=https://github.com/other/repo/actions/runs/123 sha=abc123 agents=cursor-cli",
	}
	dispatcher := &fakeGitHubDispatcher{}
	handler := newHandlerForTest(t, fetcher, dispatcher)

	body := `{"type":"event_callback","event":{"type":"message","channel":"C123","user":"U123","text":"triage e2e","ts":"111.222","thread_ts":"111.111"}}`
	req := signedRequest(t, http.MethodPost, "/slack/events", body, testSigningSecret, fixedNow())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if dispatcher.calls != 0 {
		t.Fatalf("dispatch calls = %d, want 0", dispatcher.calls)
	}
}

func fixedNow() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

func signedRequest(t *testing.T, method, target, body, secret string, now time.Time) *http.Request {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", fmt.Sprintf("%d", now.Unix()))
	req.Header.Set("X-Slack-Signature", slackSignature(secret, fmt.Sprintf("%d", now.Unix()), body))
	return req
}

func slackSignature(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":" + body))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	return newHandler(Config{
		SigningSecret: testSigningSecret,
		AllowedRepo:   "entireio/cli",
	}, &fakeSlackFetcher{}, &fakeGitHubDispatcher{}, func() time.Time {
		return fixedNow()
	})
}

func newHandlerForTest(t *testing.T, slack *fakeSlackFetcher, github *fakeGitHubDispatcher) http.Handler {
	t.Helper()
	return newHandler(Config{
		SigningSecret: testSigningSecret,
		AllowedRepo:   "entireio/cli",
	}, slack, github, func() time.Time {
		return fixedNow()
	})
}

type fakeSlackFetcher struct {
	calls    int
	channel  string
	threadTS string
	body     string
}

func (f *fakeSlackFetcher) FetchParentMessage(_ context.Context, channel, threadTS string) (string, error) {
	f.calls++
	f.channel = channel
	f.threadTS = threadTS
	return f.body, nil
}

type fakeGitHubDispatcher struct {
	calls    int
	payloads []slacktriage.DispatchPayload
}

func (f *fakeGitHubDispatcher) DispatchRepositoryEvent(_ context.Context, payload slacktriage.DispatchPayload) error {
	f.calls++
	f.payloads = append(f.payloads, payload)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
