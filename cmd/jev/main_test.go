package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	jev "github.com/stumble/jev-go"
)

func TestInteractiveVercel(t *testing.T) {
	t.Setenv("AI_GATEWAY_API_KEY", "cli-test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/evaluation-model" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer cli-test-key" {
			t.Errorf("authorization: %q", r.Header.Get("Authorization"))
		}
		var body struct {
			State     map[string]string `json:"state"`
			Questions map[string]struct {
				Type string `json:"type"`
			} `json:"questions"`
			ProviderOptions map[string]map[string]bool `json:"providerOptions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.State["ticket"] != "Please help now" || body.Questions["urgent"].Type != "boolean" {
			t.Errorf("body: %+v", body)
		}
		if !body.ProviderOptions["gateway"]["zeroDataRetention"] {
			t.Errorf("provider options: %+v", body.ProviderOptions)
		}
		_, _ = io.WriteString(
			w,
			`{"answers":{"urgent":{"type":"boolean","probability":0.9}},"usage":{"inputTokens":5,"outputTokens":1},"providerMetadata":{"gateway":{"cost":"0.1"}}}`,
		)
	}))
	defer server.Close()

	input := strings.Join([]string{
		`{"ticket":"Please help now"}`,
		"urgent",
		"noul",
		"Is this urgent?",
		"time sensitive",
		"not time sensitive",
		"",
	}, "\n") + "\n"
	var stdout, stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{
			"-provider", "vercel",
			"-base-url", server.URL,
			"-zero-data-retention",
		},
		strings.NewReader(input),
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", exitCode, &stderr, &stdout)
	}
	if !strings.Contains(stdout.String(), `"noul": 0.9`) ||
		strings.Contains(stdout.String(), "cli-test-key") {
		t.Fatalf("stdout: %s", &stdout)
	}
	if strings.Contains(stdout.String(), "provider_metadata") {
		t.Fatalf("provider metadata should be opt-in: %s", &stdout)
	}
	if strings.Contains(stderr.String(), "cli-test-key") {
		t.Fatalf("stderr exposed key: %s", &stderr)
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = run(
		context.Background(),
		[]string{
			"-provider", "vercel",
			"-base-url", server.URL,
			"-zero-data-retention",
			"-show-metadata",
		},
		strings.NewReader(input),
		&stdout,
		&stderr,
	)
	if exitCode != 0 || !strings.Contains(stdout.String(), "provider_metadata") {
		t.Fatalf("metadata output: exit=%d stdout=%s stderr=%s", exitCode, &stdout, &stderr)
	}
}

func TestInteractiveChoiceAndScore(t *testing.T) {
	input := strings.Join([]string{
		"ticket",
		"route", "choice", "Which team?",
		"billing", "payments", "other", "anything else", "",
		"urgency", "score", "How urgent?",
		"low", "high", "",
		"",
	}, "\n") + "\n"
	prompt := newPrompter(strings.NewReader(input), io.Discard)
	request, err := prompt.request("typesafe", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Questions) != 2 || request.Questions["route"].Type != jev.QuestionChoice ||
		request.Questions["urgency"].Type != jev.QuestionScore {
		t.Fatalf("questions: %+v", request.Questions)
	}
}

func TestCLIConfigurationErrors(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	tests := [][]string{
		nil,
		{"-provider", "unknown"},
		{"-timeout", "0s"},
		{"unexpected"},
	}
	for _, args := range tests {
		var stderr bytes.Buffer
		if code := run(
			context.Background(),
			args,
			strings.NewReader(""),
			io.Discard,
			&stderr,
		); code != 2 {
			t.Errorf("args=%v code=%d stderr=%s", args, code, &stderr)
		}
	}
}

func TestCLIErrorsAfterConfiguration(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "key")
	tests := []struct {
		name  string
		args  []string
		input string
		code  int
	}{
		{"insecure base URL", []string{"-base-url", "http://example.com"}, "", 2},
		{"gateway option on TypeSafe", []string{"-zero-data-retention"}, "", 2},
		{"incomplete input", nil, "state\n", 2},
		{"invalid state JSON", nil, "{invalid\n", 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(
				context.Background(),
				test.args,
				strings.NewReader(test.input),
				io.Discard,
				&stderr,
			)
			if code != test.code {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
		})
	}
}

func TestCLIAPIFailure(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"message":"bad question"}}`)
	}))
	defer server.Close()
	input := "state\nq\nnoul\nquestion\n\n\n\n"
	var stderr bytes.Buffer
	if code := run(
		context.Background(),
		[]string{"-base-url", server.URL},
		strings.NewReader(input),
		io.Discard,
		&stderr,
	); code != 1 || !strings.Contains(stderr.String(), "bad question") {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
}

func TestHelpSucceeds(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(
		context.Background(),
		[]string{"-h"},
		strings.NewReader(""),
		io.Discard,
		&stderr,
	); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if !strings.Contains(stderr.String(), "Usage: jev") {
		t.Fatalf("help: %s", &stderr)
	}
}

func TestParseState(t *testing.T) {
	structured, err := parseState(`{"count":12}`)
	if err != nil {
		t.Fatal(err)
	}
	if structured.(map[string]any)["count"].(json.Number).String() != "12" {
		t.Fatalf("structured state: %+v", structured)
	}
	text, err := parseState("plain text")
	if err != nil || text != "plain text" {
		t.Fatalf("text state: %v, %v", text, err)
	}
	if _, err := parseState(`{"count":12} {"extra":true}`); err == nil {
		t.Fatal("expected multiple JSON values to fail")
	}
}

func TestQuestionPrompts(t *testing.T) {
	t.Run("boolean alias", func(t *testing.T) {
		prompt := newPrompter(strings.NewReader("boolean\nquestion\n\nno\n"), io.Discard)
		question, err := prompt.question()
		if err != nil || question.Type != jev.QuestionNoul {
			t.Fatalf("question=%+v err=%v", question, err)
		}
	})
	t.Run("choice retries", func(t *testing.T) {
		input := "choice\nquestion\n\na\nfirst\na\nb\nsecond\n\n"
		prompt := newPrompter(strings.NewReader(input), io.Discard)
		question, err := prompt.question()
		if err != nil || question.Type != jev.QuestionChoice {
			t.Fatalf("question=%+v err=%v", question, err)
		}
	})
	t.Run("score requires two", func(t *testing.T) {
		input := "score\nquestion\n\nlow\n\nhigh\n\n"
		prompt := newPrompter(strings.NewReader(input), io.Discard)
		question, err := prompt.question()
		if err != nil || question.Type != jev.QuestionScore {
			t.Fatalf("question=%+v err=%v", question, err)
		}
	})
	t.Run("unknown type", func(t *testing.T) {
		prompt := newPrompter(strings.NewReader("unknown\nquestion\n"), io.Discard)
		if _, err := prompt.question(); err == nil {
			t.Fatal("expected unknown type to fail")
		}
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestPromptReadFailure(t *testing.T) {
	prompt := newPrompter(failingReader{}, io.Discard)
	if _, err := prompt.read("prompt"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error: %v", err)
	}
}
