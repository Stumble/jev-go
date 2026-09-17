package jev_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stumble/jev-go"
)

func newClient(t *testing.T, server *httptest.Server, retry *jev.RetryPolicy) *jev.Client {
	t.Helper()
	client, err := jev.NewClient(jev.Config{APIKey: "test-key", BaseURL: server.URL, Retry: retry})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSystemOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization: %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type: %q", got)
		}
		var body struct {
			State     map[string]string                     `json:"state"`
			Model     string                                `json:"model"`
			Questions map[string]map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "jev-latest" || body.State["document"] != "Help ASAP" {
			t.Errorf("unexpected request body: %+v", body)
		}
		for id, kind := range map[string]string{"urgent": "noul", "category": "choice", "severity": "score"} {
			if string(body.Questions[id]["type"]) != fmt.Sprintf("%q", kind) {
				t.Errorf("%s type: %s", id, body.Questions[id]["type"])
			}
		}
		var criteria map[string]any
		if err := json.Unmarshal(body.Questions["category"]["criteria"], &criteria); err != nil || len(criteria) != 2 || criteria["billing"] != nil {
			t.Errorf("choice criteria: %v, %v", criteria, err)
		}
		var levels []string
		if err := json.Unmarshal(body.Questions["severity"]["criteria"], &levels); err != nil || len(levels) != 2 {
			t.Errorf("score criteria: %v, %v", levels, err)
		}
		w.Header().Set("X-TypeSafe-Request-ID", "req-123")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"urgent":{"type":"noul","noul":0.92},"category":{"type":"choice","choice":"billing","confidence":0.81,"probabilities":{"billing":0.9,"other":0.1}},"severity":{"type":"score","score":1.6,"confidence":0.8,"legend":{"0":"low","1":"high"},"probabilities":{"0":0.4,"1":0.6}}},"usage":{"input_tokens":12,"output_tokens":3}}`)
	}))
	defer server.Close()
	client := newClient(t, server, nil)
	response, err := client.Ask(context.Background(), jev.Request{
		State: map[string]string{"document": "Help ASAP"},
		Questions: map[string]jev.Question{
			"urgent":   jev.Noul("Is this urgent?", jev.NoulCriteria{True: "time-sensitive"}),
			"category": jev.Choice("Which department?", map[string]any{"billing": nil, "other": nil}),
			"severity": jev.Score("How severe?", []string{"low", "high"}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "jev-1.13.0" || response.RequestID != "req-123" || response.Usage.InputTokens != 12 {
		t.Errorf("unexpected metadata: %+v", response)
	}
	if response.Answers["urgent"].Noul != 0.92 || response.Answers["category"].Choice != "billing" || response.Answers["severity"].Score != 1.6 {
		t.Errorf("unexpected answers: %+v", response.Answers)
	}
	if string(response.Answers["severity"].Legend["0"]) != `"low"` {
		t.Errorf("score legend: %v", response.Answers["severity"].Legend)
	}
}

func TestModelsAndOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant") != "call" {
			t.Errorf("header override: %q", r.Header.Get("X-Tenant"))
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"jev-latest","description":"stable","release_date":"2026-09-01"}]}`)
	}))
	defer server.Close()
	client, err := jev.NewClient(jev.Config{APIKey: "test", BaseURL: server.URL, Headers: http.Header{"X-Tenant": {"default"}}})
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(context.Background(), jev.CallOptions{Headers: http.Header{"X-Tenant": {"call"}}})
	if err != nil || len(models) != 1 || models[0].Name != "jev-latest" {
		t.Fatalf("models: %v, %v", models, err)
	}
}

func TestRetriesAndAPIError(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt > 1 && r.Header.Get("X-TypeSafe-Retry-Count") != fmt.Sprint(attempt-1) {
			t.Errorf("retry header on attempt %d: %s", attempt, r.Header.Get("X-TypeSafe-Retry-Count"))
		}
		if attempt < 3 {
			w.Header().Set("Retry-After-Ms", "0")
			w.WriteHeader(map[int32]int{1: 429, 2: 529}[attempt])
			_, _ = io.WriteString(w, `{"error":{"message":"try later","code":"busy"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"jev-latest","answers":{"urgent":{"type":"noul","noul":0.1}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	client := newClient(t, server, nil)
	result, err := client.SystemOne(context.Background(), jev.Request{State: "ticket", Questions: map[string]jev.Question{"urgent": jev.Noul("Urgent?")}})
	if err != nil || result.Answers["urgent"].Noul != 0.1 || attempts.Load() != 3 {
		t.Fatalf("retries: result=%+v attempts=%d err=%v", result, attempts.Load(), err)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("X-TypeSafe-Request-ID", "req-bad")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid questions","code":"invalid_request"}}`)
	}))
	defer failing.Close()
	client = newClient(t, failing, nil)
	_, err = client.SystemOne(context.Background(), jev.Request{Questions: map[string]jev.Question{"x": jev.Noul("hi")}})
	var apiErr *jev.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 422 || apiErr.RequestID != "req-bad" || apiErr.Code != "invalid_request" || !strings.Contains(err.Error(), "invalid questions") || !strings.Contains(string(apiErr.Body), "invalid_request") {
		t.Fatalf("API error: %v", err)
	}
	if attempts.Load() != 4 {
		t.Errorf("nonretryable status retried: %d total calls", attempts.Load())
	}
}

func TestCancelBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := newClient(t, server, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := client.Ask(ctx, jev.Request{State: "a", Questions: map[string]jev.Question{"x": jev.Noul("x")}})
	if !errors.Is(err, context.DeadlineExceeded) || attempts.Load() != 1 {
		t.Fatalf("cancellation: calls=%d err=%v", attempts.Load(), err)
	}
}

func TestConcurrentCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"jev-latest","answers":{"x":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	client := newClient(t, server, nil)
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response, err := client.Ask(context.Background(), jev.Request{State: "hi", Questions: map[string]jev.Question{"x": jev.Noul("x")}})
			if err != nil || response.Answers["x"].Noul != 0.5 {
				t.Errorf("concurrent call: %v, %v", response, err)
			}
		}()
	}
	group.Wait()
}

func TestRedirectCannotForwardAPIKey(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := jev.NewClient(jev.Config{
		APIKey: "test-key", BaseURL: server.URL,
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Ask(context.Background(), jev.Request{Questions: map[string]jev.Question{"x": jev.Noul("x")}})
	var apiErr *jev.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTemporaryRedirect || redirected.Load() != 0 {
		t.Fatalf("redirect followed: err=%v target calls=%d", err, redirected.Load())
	}
}

func TestTimeoutRetriesAndCallOverride(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			time.Sleep(40 * time.Millisecond)
			return
		}
		_, _ = io.WriteString(w, `{"model":"jev-latest","answers":{"x":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	client, err := jev.NewClient(jev.Config{
		APIKey: "test-key", BaseURL: server.URL,
		Retry: &jev.RetryPolicy{MaxRetries: 0, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Ask(context.Background(), jev.Request{Questions: map[string]jev.Question{"x": jev.Noul("x")}}, jev.CallOptions{
		Timeout: 5 * time.Millisecond,
		Retry:   &jev.RetryPolicy{MaxRetries: 1},
	})
	if err != nil || result.Answers["x"].Noul != 0.5 || attempts.Load() != 2 {
		t.Fatalf("timeout retry: err=%v result=%+v attempts=%d", err, result, attempts.Load())
	}
}

func TestValidateBeforeSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client := newClient(t, server, nil)
	tests := []jev.Request{
		{Questions: map[string]jev.Question{}},
		{Questions: map[string]jev.Question{"x": jev.Score("score", []string{"one"})}},
		{Questions: map[string]jev.Question{"x": jev.Choice("choose", map[string]string{})}},
		{Questions: map[string]jev.Question{"x": {Type: "unknown"}}},
		{Questions: map[string]jev.Question{" ": jev.Noul("hi")}},
		{Questions: map[string]jev.Question{"x": jev.Noul("hi")}, State: make(chan int)},
	}
	for _, request := range tests {
		_, err := client.SystemOne(context.Background(), request)
		if err == nil {
			t.Errorf("expected validation error for %+v", request)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("invalid requests reached server: %d", calls.Load())
	}
}

func TestConfiguration(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", " env-key ")
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "jev-test")
	t.Setenv("TYPESAFE_BASE_URL", "https://api.typesafe.ai")
	if _, err := jev.NewClient(jev.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := jev.NewClient(jev.Config{BaseURL: "http://example.com"}); err == nil {
		t.Fatal("expected error for plaintext remote endpoint")
	}
	if _, err := jev.NewClient(jev.Config{Retry: &jev.RetryPolicy{MaxRetries: -1}}); err == nil {
		t.Fatal("expected error for negative retries")
	}
	if _, err := jev.NewClient(jev.Config{Headers: http.Header{"Authorization": {"bad"}}}); err == nil {
		t.Fatal("expected error for reserved auth header")
	}
}
