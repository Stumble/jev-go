package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResolveRetryPolicy(t *testing.T) {
	defaults, err := resolveRetry(nil)
	if err != nil || defaults.MaxRetries != 2 || defaults.InitialBackoff != 500*time.Millisecond {
		t.Fatalf("defaults: %+v, %v", defaults, err)
	}
	custom, err := resolveRetry(&RetryPolicy{
		MaxRetries:     4,
		InitialBackoff: time.Second,
		MaxBackoff:     2 * time.Second,
		MaxRetryAfter:  3 * time.Second,
	})
	if err != nil || custom.MaxRetries != 4 || custom.MaxRetryAfter != 3*time.Second {
		t.Fatalf("custom: %+v, %v", custom, err)
	}
	for _, policy := range []*RetryPolicy{
		{MaxRetries: -1},
		{InitialBackoff: -1},
		{MaxBackoff: -1},
		{MaxRetryAfter: -1},
		{InitialBackoff: 2 * time.Second, MaxBackoff: time.Second},
	} {
		if _, err := resolveRetry(policy); err == nil {
			t.Errorf("expected invalid policy to fail: %+v", policy)
		}
	}
}

func TestRetryDelays(t *testing.T) {
	policy := RetryPolicy{
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     400 * time.Millisecond,
		MaxRetryAfter:  2 * time.Second,
	}
	tests := []struct {
		name    string
		header  http.Header
		attempt int
		min     time.Duration
		max     time.Duration
	}{
		{
			"milliseconds",
			http.Header{"Retry-After-Ms": {"125"}},
			0,
			125 * time.Millisecond,
			125 * time.Millisecond,
		},
		{"seconds", http.Header{"Retry-After": {"1"}}, 0, time.Second, time.Second},
		{
			"invalid falls back",
			http.Header{"Retry-After": {"invalid"}},
			0,
			75 * time.Millisecond,
			100 * time.Millisecond,
		},
		{
			"too long falls back",
			http.Header{"Retry-After": {"3"}},
			1,
			150 * time.Millisecond,
			200 * time.Millisecond,
		},
		{"capped backoff", nil, 20, 300 * time.Millisecond, 400 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delay := retryDelay(test.attempt, test.header, policy)
			if delay < test.min || delay > test.max {
				t.Fatalf("delay %s outside [%s, %s]", delay, test.min, test.max)
			}
		})
	}
	past := http.Header{"Retry-After": {time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)}}
	if delay := retryDelay(0, past, policy); delay != 0 {
		t.Fatalf("past Retry-After: %s", delay)
	}
}

func TestParseAPIErrorShapes(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
		wantCode    string
	}{
		{"invalid JSON", "not-json", "Unprocessable Entity", ""},
		{"message", `{"message":"bad","code":"top"}`, "bad", "top"},
		{"detail", `{"detail":"details"}`, "details", ""},
		{"error string", `{"error":"broken"}`, "broken", ""},
		{"nested", `{"error":{"message":"nested","code":"inner"}}`, "nested", "inner"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := parseAPIError(http.StatusUnprocessableEntity, "request-id", []byte(test.body))
			if err.Message != test.wantMessage || err.Code != test.wantCode ||
				err.RequestID != "request-id" || string(err.Body) != test.body {
				t.Fatalf("error: %+v", err)
			}
			if !strings.Contains(err.Error(), "request request-id") {
				t.Fatalf("formatted error: %s", err)
			}
		})
	}
	var nilError *APIError
	if nilError.Error() != "jev: API error" {
		t.Fatalf("nil error: %q", nilError.Error())
	}
}

func TestAnswerJSONShapes(t *testing.T) {
	zero := 0.0
	legend := map[string]json.RawMessage{"0": json.RawMessage(`"low"`)}
	tests := []struct {
		name   string
		answer Answer
		want   string
	}{
		{"noul zero", Answer{Type: QuestionNoul}, `{"type":"noul","noul":0}`},
		{
			"choice no confidence",
			Answer{Type: QuestionChoice, Choice: "a"},
			`{"type":"choice","choice":"a"}`,
		},
		{
			"choice zero confidence",
			Answer{Type: QuestionChoice, Choice: "a", Confidence: zero, HasConfidence: true},
			`{"type":"choice","choice":"a","confidence":0}`,
		},
		{
			"score",
			Answer{Type: QuestionScore, Legend: legend},
			`{"type":"score","score":0,"legend":{"0":"low"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.answer)
			if err != nil || string(encoded) != test.want {
				t.Fatalf("got %s, want %s: %v", encoded, test.want, err)
			}
		})
	}
	if _, err := json.Marshal(Answer{Type: "unknown"}); err == nil {
		t.Fatal("expected unsupported answer type to fail")
	}
	for _, input := range []string{
		`not-json`,
		`{"type":"unknown"}`,
		`{"type":"choice","choice":"a","confidence":2,"probabilities":{"a":1}}`,
		`{"type":"choice","choice":"a","confidence":1,"probabilities":{"a":2}}`,
	} {
		var answer Answer
		if err := json.Unmarshal([]byte(input), &answer); err == nil {
			t.Errorf("expected invalid answer to fail: %s", input)
		}
	}
}

func TestWaitWithReadyContextAndZeroDelay(t *testing.T) {
	if err := wait(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wait(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error: %v", err)
	}
}
