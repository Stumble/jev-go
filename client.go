package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL     = "https://api.typesafe.ai"
	defaultModel       = "jev-latest"
	vercelBaseURL      = "https://ai-gateway.vercel.sh/v4/ai"
	vercelDefaultModel = "typesafe-ai/jev"
	maxResponseSize    = 4 << 20
)

// Provider selects the upstream evaluation protocol.
type Provider string

const (
	ProviderTypeSafe Provider = "typesafe" // Config's default; TypeSafe's public API
	ProviderVercel   Provider = "vercel"   // experimental AI Gateway evaluation transport
)

// Config controls a client. Environment variables are never consulted unless
// ReadFromEnvironment is true. Explicit values take precedence when enabled.
// A Client and its HTTPClient may be shared by concurrent goroutines.
type Config struct {
	Provider     Provider
	APIKey       string
	BaseURL      string
	DefaultModel string

	// ReadFromEnvironment opts into the selected provider's environment
	// fallbacks. TypeSafe uses TYPESAFE_*; Vercel uses AI_GATEWAY_API_KEY.
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

// Client evaluates questions through the configured provider.
type Client struct {
	provider     Provider
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
	provider := cfg.Provider
	if provider == "" {
		provider = ProviderTypeSafe
	}
	keyEnv, baseEnv, modelEnv := "TYPESAFE_API_KEY", "TYPESAFE_BASE_URL", "TYPESAFE_DEFAULT_MODEL"
	baseDefault, modelDefault := defaultBaseURL, defaultModel
	switch provider {
	case ProviderTypeSafe:
	case ProviderVercel:
		keyEnv, baseEnv, modelEnv = "AI_GATEWAY_API_KEY", "", ""
		baseDefault, modelDefault = vercelBaseURL, vercelDefaultModel
	default:
		return nil, fmt.Errorf("jev: unsupported provider %q", provider)
	}
	key := configured(cfg.APIKey, keyEnv, "", cfg.ReadFromEnvironment)
	if key == "" {
		return nil, errors.New(
			"jev: API key is required (set Config.APIKey, or opt into ReadFromEnvironment)",
		)
	}
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("jev: API key must not contain newlines")
	}
	base := configured(cfg.BaseURL, baseEnv, baseDefault, cfg.ReadFromEnvironment)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname()))) {
		return nil, errors.New(
			"jev: base URL must be HTTPS or loopback HTTP, with no credentials, query, or fragment",
		)
	}
	if provider == ProviderTypeSafe && strings.EqualFold(u.Hostname(), "ai-gateway.vercel.sh") {
		return nil, errors.New(
			"jev: choose ProviderVercel to use AI Gateway evaluation",
		)
	}
	if provider == ProviderVercel && strings.EqualFold(u.Hostname(), "api.typesafe.ai") {
		return nil, errors.New("jev: Vercel credentials cannot be sent to TypeSafe's API")
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
		provider: provider,
		apiKey:   key,
		baseURL:  strings.TrimRight(base, "/"),
		defaultModel: configured(
			cfg.DefaultModel,
			modelEnv,
			modelDefault,
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
	if readFromEnv && env != "" {
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
	if c.provider != ProviderVercel && request.Gateway != nil {
		return result, errors.New("jev: Gateway options require ProviderVercel")
	}
	if request.Model == "" {
		request.Model = c.defaultModel
	}
	var body []byte
	var path string
	var err error
	if c.provider == ProviderVercel {
		body, err = marshalVercelRequest(request)
		path = "/evaluation-model"
	} else {
		body, err = json.Marshal(request)
		path = "/v1/systemone"
	}
	if err != nil {
		return result, fmt.Errorf("jev: encode request: %w", err)
	}
	data, requestID, err := c.do(ctx, http.MethodPost, path, body, request.Model, options)
	if err != nil {
		return result, err
	}
	if c.provider == ProviderVercel {
		result, err = decodeVercelResponse(data, request)
	} else {
		err = json.Unmarshal(data, &result)
	}
	if err != nil {
		return Response{}, fmt.Errorf("jev: decode evaluation: %w", err)
	}
	if result.Answers == nil || result.Model == "" {
		return Response{}, errors.New("jev: evaluation response is missing model or answers")
	}
	if err := validateAnswers(request.Questions, result.Answers, result.Rounding); err != nil {
		return Response{}, err
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
	if c.provider == ProviderVercel {
		return nil, errors.New("jev: ListModels is supported only by ProviderTypeSafe")
	}
	data, _, err := c.do(ctx, http.MethodGet, "/v1/models", nil, "", options)
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
			for _, key := range v.MapKeys() {
				if strings.TrimSpace(key.String()) == "" {
					return fmt.Errorf("jev: choice question %q has a blank option", name)
				}
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

func validateAnswers(
	questions map[string]Question,
	answers map[string]Answer,
	rounding *Rounding,
) error {
	for _, name := range sortedQuestionNames(questions) {
		question := questions[name]
		answer, ok := answers[name]
		if !ok || answer.Type != question.Type {
			return fmt.Errorf("jev: evaluation response is missing or mismatches question %q", name)
		}
		if err := validateAnswer(question, answer, rounding); err != nil {
			return fmt.Errorf("jev: invalid answer %q: %w", name, err)
		}
	}
	if len(answers) != len(questions) {
		return errors.New("jev: evaluation response contains unexpected answers")
	}
	return nil
}

func validateAnswer(question Question, answer Answer, rounding *Rounding) error {
	switch question.Type {
	case QuestionNoul:
		return nil
	case QuestionChoice:
		criteria := reflect.ValueOf(question.Criteria)
		choice := reflect.ValueOf(answer.Choice).Convert(criteria.Type().Key())
		if !criteria.MapIndex(choice).IsValid() {
			return fmt.Errorf("choice %q is not one of the requested options", answer.Choice)
		}
		if len(answer.Probabilities) == 0 {
			return nil
		}
		if len(answer.Probabilities) != criteria.Len() {
			return errors.New("choice probabilities do not cover every requested option")
		}
		for label := range answer.Probabilities {
			key := reflect.ValueOf(label).Convert(criteria.Type().Key())
			if !criteria.MapIndex(key).IsValid() {
				return fmt.Errorf("choice probability %q is not a requested option", label)
			}
		}
		return validateDistribution(answer.Probabilities, rounding)
	case QuestionScore:
		levels := reflect.ValueOf(question.Criteria).Len()
		if answer.Score < 0 || answer.Score > float64(levels-1) {
			return errors.New("score is outside the requested levels")
		}
		if len(answer.Probabilities) > 0 {
			if len(answer.Probabilities) != levels {
				return errors.New("score probabilities do not cover every requested level")
			}
			for i := 0; i < levels; i++ {
				if _, ok := answer.Probabilities[strconv.Itoa(i)]; !ok {
					return fmt.Errorf("score probabilities are missing level %d", i)
				}
			}
			if err := validateDistribution(answer.Probabilities, rounding); err != nil {
				return err
			}
		}
		if len(answer.Legend) > 0 {
			if len(answer.Legend) != levels {
				return errors.New("score legend does not cover every requested level")
			}
			for i := 0; i < levels; i++ {
				label := strconv.Itoa(i)
				actual, ok := answer.Legend[label]
				if !ok {
					return fmt.Errorf("score legend is missing level %d", i)
				}
				expected, err := json.Marshal(
					reflect.ValueOf(question.Criteria).Index(i).Interface(),
				)
				if err != nil {
					return fmt.Errorf("encode requested score level %d: %w", i, err)
				}
				if !equalJSON(actual, expected) {
					return fmt.Errorf("score legend level %d does not match the request", i)
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported question type %q", question.Type)
	}
}

func equalJSON(left, right []byte) bool {
	leftValue, leftErr := decodeComparableJSON(left)
	rightValue, rightErr := decodeComparableJSON(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func decodeComparableJSON(data []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func validateDistribution(probabilities map[string]float64, rounding *Rounding) error {
	total := 0.0
	for _, value := range probabilities {
		total += value
	}
	tolerance := 1e-6
	if rounding != nil && rounding.ProbabilityDecimals != nil {
		decimals := *rounding.ProbabilityDecimals
		if decimals >= 0 && decimals <= 15 {
			tolerance = float64(len(probabilities))*0.5*math.Pow10(-decimals) + 1e-12
		}
	}
	if math.Abs(total-1) > tolerance {
		return fmt.Errorf("probabilities sum to %g instead of 1", total)
	}
	return nil
}

func (c *Client) do(
	ctx context.Context,
	method, path string,
	body []byte,
	model string,
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
		if c.provider == ProviderVercel {
			req.Header.Set("Ai-Evaluation-Model-Specification-Version", "4")
			req.Header.Set("Ai-Model-Id", model)
			req.Header.Set("Ai-Gateway-Protocol-Version", "0.0.1")
			req.Header.Set("Ai-Gateway-Auth-Method", "api-key")
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if attempt > 0 && c.provider == ProviderTypeSafe {
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
		case "authorization",
			"content-type",
			"accept",
			"host",
			"x-typesafe-retry-count",
			"ai-model-id",
			"ai-evaluation-model-specification-version",
			"ai-gateway-protocol-version",
			"ai-gateway-auth-method":
			return fmt.Errorf("jev: header %q is managed by the client", name)
		}
	}
	return nil
}
