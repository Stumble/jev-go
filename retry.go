package jev

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy controls automatic retries for connection errors, timeouts,
// HTTP 408, 429, and 5xx (including 529). MaxRetries counts attempts after
// the first; use &RetryPolicy{MaxRetries: 0} to disable retries.
// Zero-valued duration fields inherit their defaults.
type RetryPolicy struct {
	MaxRetries     int
	InitialBackoff time.Duration // default 500ms
	MaxBackoff     time.Duration // default 5s
	MaxRetryAfter  time.Duration // default 60s; longer server delays use backoff
}

func resolveRetry(policy *RetryPolicy) (RetryPolicy, error) {
	result := RetryPolicy{
		MaxRetries:     2,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
		MaxRetryAfter:  60 * time.Second,
	}
	if policy == nil {
		return result, nil
	}
	if policy.MaxRetries < 0 || policy.InitialBackoff < 0 || policy.MaxBackoff < 0 ||
		policy.MaxRetryAfter < 0 {
		return RetryPolicy{}, errors.New("jev: retry counts and delays cannot be negative")
	}
	result.MaxRetries = policy.MaxRetries
	if policy.InitialBackoff > 0 {
		result.InitialBackoff = policy.InitialBackoff
	}
	if policy.MaxBackoff > 0 {
		result.MaxBackoff = policy.MaxBackoff
	}
	if policy.MaxRetryAfter > 0 {
		result.MaxRetryAfter = policy.MaxRetryAfter
	}
	if result.MaxBackoff < result.InitialBackoff {
		return RetryPolicy{}, errors.New(
			"jev: maximum backoff must be at least the initial backoff",
		)
	}
	return result, nil
}

func retryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests ||
		status >= 500 && status <= 599
}

func backoff(attempt int, policy RetryPolicy) time.Duration {
	delay := policy.InitialBackoff
	for i := 0; i < attempt && delay < policy.MaxBackoff; i++ {
		if delay > policy.MaxBackoff/2 {
			delay = policy.MaxBackoff
		} else {
			delay *= 2
		}
	}
	if delay > policy.MaxBackoff {
		delay = policy.MaxBackoff
	}
	// #nosec G404 -- backoff jitter does not protect secrets or select security-sensitive values.
	return time.Duration(float64(delay) * (1 - 0.25*rand.Float64()))
}

func retryDelay(attempt int, headers http.Header, policy RetryPolicy) time.Duration {
	if raw := strings.TrimSpace(headers.Get("Retry-After-Ms")); raw != "" {
		if ms, err := strconv.ParseInt(
			raw,
			10,
			64,
		); err == nil && ms >= 0 &&
			ms <= int64(policy.MaxRetryAfter/time.Millisecond) {
			return time.Duration(ms) * time.Millisecond
		}
	}
	if raw := strings.TrimSpace(headers.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseInt(
			raw,
			10,
			64,
		); err == nil && seconds >= 0 &&
			seconds <= int64(policy.MaxRetryAfter/time.Second) {
			return time.Duration(seconds) * time.Second
		}
		if date, err := http.ParseTime(raw); err == nil {
			delay := time.Until(date)
			if delay < 0 {
				return 0
			}
			if delay <= policy.MaxRetryAfter {
				return delay
			}
		}
	}
	return backoff(attempt, policy)
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// APIError describes a non-success HTTP response. Use errors.As to inspect
// StatusCode, RequestID, and Code without parsing an error string.
type APIError struct {
	StatusCode int
	RequestID  string
	Code       string
	Message    string
	Body       []byte // bounded raw response for structured validation errors; never logged by the SDK
}

func (e *APIError) Error() string {
	if e == nil {
		return "jev: API error"
	}
	message := fmt.Sprintf("jev: HTTP %d", e.StatusCode)
	if e.Message != "" {
		message += ": " + e.Message
	}
	if e.RequestID != "" {
		message += " (request " + e.RequestID + ")"
	}
	return message
}
