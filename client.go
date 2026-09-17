package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	defaultBaseURL  = "https://api.typesafe.ai"
	defaultModel    = "jev-latest"
	maxResponseSize = 4 << 20
)

// Config controls a client. Environment variables are never consulted unless
// ReadFromEnvironment is true. Explicit values take precedence when enabled.
// A Client and its HTTPClient may be shared by concurrent goroutines.
type Config struct {
	APIKey       string
	BaseURL      string
	DefaultModel string

	// ReadFromEnvironment opts into TYPESAFE_API_KEY, TYPESAFE_BASE_URL, and
	// TYPESAFE_DEFAULT_MODEL as fallbacks for unspecified fields.
	ReadFromEnvironment bool
	HTTPClient          *http.Client
	Timeout             time.Duration // per attempt; default 10 seconds
	Retry               *RetryPolicy  // nil uses the default policy; MaxRetries: 0 disables retries
	Headers             http.Header   // optional additional headers; copied at construction
}

// CallOptions override client settings for one call. Context controls the
// total deadline, including retries and backoff.
type CallOptions struct {
	Timeout time.Duration
	Retry   *RetryPolicy
	Headers http.Header
}

// Client evaluates questions through the TypeSafe AI API.
type Client struct {
	apiKey       string
	baseURL      string
	defaultModel string
	httpClient   *http.Client
	timeout      time.Duration
	retry        RetryPolicy
	headers      http.Header
}

// NewClient constructs a client without making a network request.
// The API key must come from Config (or the environment when opted in).
// Only HTTPS and local loopback HTTP endpoints are accepted to keep
// credentials off plaintext links.
func NewClient(cfg Config) (*Client, error) {
	key := configured(cfg.APIKey, "TYPESAFE_API_KEY", "", cfg.ReadFromEnvironment)
	if key == "" {
		return nil, errors.New(
			"jev: API key is required (set Config.APIKey, or opt into ReadFromEnvironment)",
		)
	}
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("jev: API key must not contain newlines")
	}
	base := configured(cfg.BaseURL, "TYPESAFE_BASE_URL", defaultBaseURL, cfg.ReadFromEnvironment)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname()))) {
		return nil, errors.New(
			"jev: base URL must be HTTPS or loopback HTTP, with no credentials, query, or fragment",
		)
	}
	if strings.EqualFold(u.Hostname(), "ai-gateway.vercel.sh") {
		return nil, errors.New(
			"jev: Vercel AI Gateway does not expose Jev evaluation through the TypeSafe REST API",
		)
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("jev: timeout cannot be negative")
	}
	policy, err := resolveRetry(cfg.Retry)
	if err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// Never forward a bearer token to an endpoint chosen by a redirect.
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if err := validateHeaders(cfg.Headers); err != nil {
		return nil, err
	}
	return &Client{
		apiKey:  key,
		baseURL: strings.TrimRight(base, "/"),
		defaultModel: configured(
			cfg.DefaultModel,
			"TYPESAFE_DEFAULT_MODEL",
			defaultModel,
			cfg.ReadFromEnvironment,
		),
		httpClient: &clientCopy,
		timeout:    timeout,
		retry:      policy,
		headers:    cfg.Headers.Clone(),
	}, nil
}

func configured(explicit, env, fallback string, readFromEnv bool) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	if readFromEnv {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return fallback
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SystemOne evaluates named questions, returning their answers and token usage.
// Callers can set a total time budget with context.WithTimeout.
func (c *Client) SystemOne(
	ctx context.Context,
	request Request,
	options ...CallOptions,
) (Response, error) {
	var result Response
	if c == nil {
		return result, errors.New("jev: nil client")
	}
	if err := validateQuestions(request.Questions); err != nil {
		return result, err
	}
	if request.Model == "" {
		request.Model = c.defaultModel
	}
	body, err := json.Marshal(request)
	if err != nil {
		return result, fmt.Errorf("jev: encode request: %w", err)
	}
	data, requestID, err := c.do(ctx, http.MethodPost, "/v1/systemone", body, options)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return Response{}, fmt.Errorf("jev: decode evaluation: %w", err)
	}
	if result.Answers == nil || result.Model == "" {
		return Response{}, errors.New("jev: evaluation response is missing model or answers")
	}
	for _, name := range sortedQuestionNames(request.Questions) {
		question := request.Questions[name]
		answer, ok := result.Answers[name]
		if !ok || answer.Type != question.Type {
			return Response{}, fmt.Errorf(
				"jev: evaluation response is missing or mismatches question %q",
				name,
			)
		}
	}
	if len(result.Answers) != len(request.Questions) {
		return Response{}, errors.New("jev: evaluation response contains unexpected answers")
	}
	result.RequestID = requestID
	return result, nil
}

// Ask is a convenience alias for SystemOne.
func (c *Client) Ask(
	ctx context.Context,
	request Request,
	options ...CallOptions,
) (Response, error) {
	return c.SystemOne(ctx, request, options...)
}

// ListModels returns the models and aliases available to the account.
func (c *Client) ListModels(ctx context.Context, options ...CallOptions) ([]Model, error) {
	if c == nil {
		return nil, errors.New("jev: nil client")
	}
	data, _, err := c.do(ctx, http.MethodGet, "/v1/models", nil, options)
	if err != nil {
		return nil, err
	}
	var result struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("jev: decode models: %w", err)
	}
	if result.Models == nil {
		return nil, errors.New("jev: models response is missing models")
	}
	return result.Models, nil
}

func validateQuestions(questions map[string]Question) error {
	if len(questions) == 0 {
		return errors.New("jev: provide at least one question")
	}
	for _, name := range sortedQuestionNames(questions) {
		q := questions[name]
		if strings.TrimSpace(name) == "" {
			return errors.New("jev: question names cannot be blank")
		}
		switch q.Type {
		case QuestionNoul:
			// Instructions or outcome descriptions may be omitted.
		case QuestionChoice:
			v := reflect.ValueOf(q.Criteria)
			if !v.IsValid() || v.Kind() != reflect.Map || v.Type().Key().Kind() != reflect.String ||
				v.Len() == 0 {
				return fmt.Errorf("jev: choice question %q requires named options", name)
			}
			if v.Len() > 255 {
				return fmt.Errorf("jev: choice question %q exceeds 255 options", name)
			}
		case QuestionScore:
			v := reflect.ValueOf(q.Criteria)
			if !v.IsValid() || (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) ||
				v.Len() < 2 {
				return fmt.Errorf("jev: score question %q requires at least two levels", name)
			}
			if v.Len() > 10 {
				return fmt.Errorf("jev: score question %q exceeds 10 levels", name)
			}
		default:
			return fmt.Errorf("jev: question %q has unsupported type %q", name, q.Type)
		}
	}
	return nil
}

func sortedQuestionNames(questions map[string]Question) []string {
	names := make([]string, 0, len(questions))
	for name := range questions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Client) do(
	ctx context.Context,
	method, path string,
	body []byte,
	options []CallOptions,
) ([]byte, string, error) {
	if ctx == nil {
		return nil, "", errors.New("jev: context cannot be nil")
	}
	if len(options) > 1 {
		return nil, "", errors.New("jev: provide at most one CallOptions")
	}
	opt := CallOptions{}
	if len(options) == 1 {
		opt = options[0]
	}
	if opt.Timeout < 0 {
		return nil, "", errors.New("jev: timeout cannot be negative")
	}
	if err := validateHeaders(opt.Headers); err != nil {
		return nil, "", err
	}
	policy := c.retry
	if opt.Retry != nil {
		var err error
		override := *opt.Retry
		if override.InitialBackoff == 0 {
			override.InitialBackoff = policy.InitialBackoff
		}
		if override.MaxBackoff == 0 {
			override.MaxBackoff = policy.MaxBackoff
		}
		if override.MaxRetryAfter == 0 {
			override.MaxRetryAfter = policy.MaxRetryAfter
		}
		policy, err = resolveRetry(&override)
		if err != nil {
			return nil, "", err
		}
	}
	timeout := c.timeout
	if opt.Timeout > 0 {
		timeout = opt.Timeout
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		var payload io.Reader
		if body != nil {
			payload = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(attemptCtx, method, c.baseURL+path, payload)
		if err != nil {
			cancel()
			return nil, "", fmt.Errorf("jev: create request: %w", err)
		}
		req.Header = c.headers.Clone()
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		for key, values := range opt.Headers {
			req.Header.Del(key)
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if attempt > 0 {
			req.Header.Set("X-TypeSafe-Retry-Count", fmt.Sprint(attempt))
		}
		res, err := c.httpClient.Do(req)
		var data []byte
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(res.Body, maxResponseSize+1))
			closeErr := res.Body.Close()
			if err == nil {
				err = closeErr
			}
		}
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			if attempt >= policy.MaxRetries {
				return nil, "", fmt.Errorf("jev: request failed: %w", err)
			}
			if err := wait(ctx, backoff(attempt, policy)); err != nil {
				return nil, "", err
			}
			continue
		}
		if len(data) > maxResponseSize {
			return nil, "", errors.New("jev: response exceeds 4 MiB")
		}
		requestID := res.Header.Get("X-TypeSafe-Request-ID")
		if requestID == "" {
			requestID = res.Header.Get("X-Request-ID")
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return data, requestID, nil
		}
		apiErr := parseAPIError(res.StatusCode, requestID, data)
		if attempt >= policy.MaxRetries || !retryable(res.StatusCode) {
			return nil, requestID, apiErr
		}
		if err := wait(ctx, retryDelay(attempt, res.Header, policy)); err != nil {
			return nil, "", err
		}
	}
}

func validateHeaders(headers http.Header) error {
	for name := range headers {
		switch strings.ToLower(name) {
		case "authorization", "content-type", "accept", "host", "x-typesafe-retry-count":
			return fmt.Errorf("jev: header %q is managed by the client", name)
		}
	}
	return nil
}
