package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/entireio/cli/internal/slacktriage"
)

const (
	defaultAddr                = ":8080"
	defaultGitHubAPIBaseURL    = "https://api.github.com"
	defaultSlackAPIBaseURL     = "https://slack.com/api"
	defaultSlackEventType      = "slack_e2e_triage_requested"
	defaultRequestTolerance    = 5 * time.Minute
	slackTimestampHeader       = "X-Slack-Request-Timestamp"
	slackSignatureHeader       = "X-Slack-Signature"
	slackEventTypeURLVerify    = "url_verification"
	slackEventTypeCallback     = "event_callback"
	slackInnerEventTypeMessage = "message"
)

// Config holds runtime settings loaded from the environment.
type Config struct {
	Addr             string
	SigningSecret    string
	SlackBotToken    string
	GitHubToken      string
	AllowedRepo      string
	GitHubEventType  string
	SlackAPIBaseURL  string
	GitHubAPIBaseURL string
	RequestTolerance time.Duration
}

func main() {
	cfg, err := loadConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	handler := newHandler(
		cfg,
		newSlackHTTPClient(cfg.SlackBotToken, cfg.SlackAPIBaseURL),
		newGitHubHTTPDispatcher(cfg.GitHubToken, cfg.GitHubAPIBaseURL, cfg.GitHubEventType, cfg.AllowedRepo),
		time.Now,
	)

	mux := http.NewServeMux()
	mux.Handle("/slack/events", handler)

	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}

func loadConfigFromEnv() (Config, error) {
	cfg := Config{
		Addr:             getEnvDefault("ADDR", defaultAddr),
		SigningSecret:    os.Getenv("SLACK_SIGNING_SECRET"),
		SlackBotToken:    os.Getenv("SLACK_BOT_TOKEN"),
		GitHubToken:      os.Getenv("GITHUB_TOKEN"),
		AllowedRepo:      getEnvFirst("ALLOWED_REPOSITORY", "GITHUB_REPOSITORY"),
		GitHubEventType:  getEnvDefault("GITHUB_EVENT_TYPE", defaultSlackEventType),
		SlackAPIBaseURL:  getEnvDefault("SLACK_API_BASE_URL", defaultSlackAPIBaseURL),
		GitHubAPIBaseURL: getEnvDefault("GITHUB_API_BASE_URL", defaultGitHubAPIBaseURL),
		RequestTolerance: defaultRequestTolerance,
	}

	if tolerance := os.Getenv("SLACK_REQUEST_TOLERANCE"); tolerance != "" {
		d, err := time.ParseDuration(tolerance)
		if err != nil {
			return Config{}, fmt.Errorf("parse SLACK_REQUEST_TOLERANCE: %w", err)
		}
		cfg.RequestTolerance = d
	}

	switch {
	case cfg.SigningSecret == "":
		return Config{}, errors.New("SLACK_SIGNING_SECRET is required")
	case cfg.SlackBotToken == "":
		return Config{}, errors.New("SLACK_BOT_TOKEN is required")
	case cfg.GitHubToken == "":
		return Config{}, errors.New("GITHUB_TOKEN is required")
	case cfg.AllowedRepo == "":
		return Config{}, errors.New("ALLOWED_REPOSITORY or GITHUB_REPOSITORY is required")
	default:
		return cfg, nil
	}
}

func getEnvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvFirst(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

type SlackMessageFetcher interface {
	FetchParentMessage(ctx context.Context, channel, threadTS string) (string, error)
}

type GitHubDispatcher interface {
	DispatchRepositoryEvent(ctx context.Context, payload slacktriage.DispatchPayload) error
}

type triageHandler struct {
	cfg        Config
	slack      SlackMessageFetcher
	github     GitHubDispatcher
	now        func() time.Time
	maxBodyLen int64
}

func newHandler(cfg Config, slack SlackMessageFetcher, github GitHubDispatcher, now func() time.Time) *triageHandler {
	if now == nil {
		now = time.Now
	}
	if cfg.RequestTolerance <= 0 {
		cfg.RequestTolerance = defaultRequestTolerance
	}

	return &triageHandler{
		cfg:        cfg,
		slack:      slack,
		github:     github,
		now:        now,
		maxBodyLen: 1 << 20,
	}
}

func (h *triageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxBodyLen))
	if err != nil {
		http.Error(w, "read request body", http.StatusBadRequest)
		return
	}

	if err := h.verifyRequest(r, body); err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	var envelope slackEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "decode slack payload", http.StatusBadRequest)
		return
	}

	switch envelope.Type {
	case slackEventTypeURLVerify:
		writeJSON(w, http.StatusOK, map[string]string{"challenge": envelope.Challenge})
	case slackEventTypeCallback:
		if err := h.handleEvent(r.Context(), envelope.Event); err != nil {
			http.Error(w, "process slack event", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (h *triageHandler) verifyRequest(r *http.Request, body []byte) error {
	timestamp := r.Header.Get(slackTimestampHeader)
	signature := r.Header.Get(slackSignatureHeader)
	if timestamp == "" || signature == "" {
		return errors.New("missing slack signature headers")
	}

	parsed, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid slack timestamp: %w", err)
	}

	requestTime := time.Unix(parsed, 0)
	now := h.now()
	if absDuration(now.Sub(requestTime)) > h.cfg.RequestTolerance {
		return errors.New("stale slack request")
	}

	mac := hmac.New(sha256.New, []byte(h.cfg.SigningSecret))
	if _, err := mac.Write([]byte("v0:" + timestamp + ":" + string(body))); err != nil {
		return fmt.Errorf("sign slack request: %w", err)
	}
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return errors.New("invalid slack signature")
	}

	return nil
}

func (h *triageHandler) handleEvent(ctx context.Context, event slackEvent) error {
	if event.Type != slackInnerEventTypeMessage {
		return nil
	}
	if event.Subtype != "" || event.BotID != "" {
		return nil
	}
	if event.ThreadTS == "" || event.ThreadTS == event.Ts {
		return nil
	}
	if !slacktriage.IsTriageTrigger(event.Text) {
		return nil
	}
	if h.slack == nil {
		return errors.New("slack fetcher is not configured")
	}
	if h.github == nil {
		return errors.New("github dispatcher is not configured")
	}
	if event.Channel == "" {
		return errors.New("channel is required")
	}

	parentBody, err := h.slack.FetchParentMessage(ctx, event.Channel, event.ThreadTS)
	if err != nil {
		return err
	}

	metadata, err := slacktriage.ParseParentMessageMetadata(parentBody)
	if err != nil {
		return err
	}
	if h.cfg.AllowedRepo != "" && metadata.Repo != h.cfg.AllowedRepo {
		return nil
	}

	payload := slacktriage.NewDispatchPayload(metadata, event.Channel, event.ThreadTS, event.User)
	return h.github.DispatchRepositoryEvent(ctx, payload)
}

type slackEnvelope struct {
	Type      string     `json:"type"`
	Challenge string     `json:"challenge,omitempty"`
	Event     slackEvent `json:"event,omitempty"`
}

type slackEvent struct {
	Type     string `json:"type,omitempty"`
	Subtype  string `json:"subtype,omitempty"`
	BotID    string `json:"bot_id,omitempty"`
	Channel  string `json:"channel,omitempty"`
	User     string `json:"user,omitempty"`
	Text     string `json:"text,omitempty"`
	Ts       string `json:"ts,omitempty"`
	ThreadTS string `json:"thread_ts,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

type slackHTTPClient struct {
	token   string
	baseURL string
	client  *http.Client
}

func newSlackHTTPClient(token, baseURL string) *slackHTTPClient {
	if baseURL == "" {
		baseURL = defaultSlackAPIBaseURL
	}
	return &slackHTTPClient{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *slackHTTPClient) FetchParentMessage(ctx context.Context, channel, threadTS string) (string, error) {
	endpoint, err := url.Parse(c.baseURL + "/conversations.replies")
	if err != nil {
		return "", err
	}

	query := endpoint.Query()
	query.Set("channel", channel)
	query.Set("ts", threadTS)
	query.Set("inclusive", "true")
	query.Set("limit", "1")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("slack conversations.replies returned %s", resp.Status)
	}

	var payload struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error,omitempty"`
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if !payload.OK {
		if payload.Error == "" {
			payload.Error = "unknown error"
		}
		return "", fmt.Errorf("slack conversations.replies error: %s", payload.Error)
	}
	if len(payload.Messages) == 0 {
		return "", errors.New("slack conversations.replies returned no messages")
	}
	return payload.Messages[0].Text, nil
}

type githubHTTPDispatcher struct {
	token      string
	baseURL    string
	eventType  string
	repository string
	client     *http.Client
}

func newGitHubHTTPDispatcher(token, baseURL, eventType, repository string) *githubHTTPDispatcher {
	if baseURL == "" {
		baseURL = defaultGitHubAPIBaseURL
	}
	if eventType == "" {
		eventType = defaultSlackEventType
	}
	return &githubHTTPDispatcher{
		token:      token,
		baseURL:    strings.TrimRight(baseURL, "/"),
		eventType:  eventType,
		repository: repository,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *githubHTTPDispatcher) DispatchRepositoryEvent(ctx context.Context, payload slacktriage.DispatchPayload) error {
	body, err := json.Marshal(struct {
		EventType     string                      `json:"event_type"`
		ClientPayload slacktriage.DispatchPayload `json:"client_payload"`
	}{
		EventType:     d.eventType,
		ClientPayload: payload,
	})
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/repos/%s/dispatches", d.baseURL, d.repository)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(responseBody) > 0 {
			return fmt.Errorf("github dispatch failed: %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
		}
		return fmt.Errorf("github dispatch failed: %s", resp.Status)
	}

	return nil
}
