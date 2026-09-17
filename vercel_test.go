package jev_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	jev "github.com/stumble/jev-go"
)

func TestVercelProvider(t *testing.T) {
	t.Setenv("AI_GATEWAY_API_KEY", "gateway-key")
	if _, err := jev.NewClient(jev.Config{Provider: jev.ProviderVercel}); err == nil {
		t.Fatal("Vercel provider must not read AI_GATEWAY_API_KEY without explicit opt-in")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/evaluation-model" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		wantHeaders := map[string]string{
			"Authorization": "Bearer gateway-key",
			"Ai-Evaluation-Model-Specification-Version": "4",
			"Ai-Model-Id":                 "typesafe-ai/jev",
			"Ai-Gateway-Protocol-Version": "0.0.1",
			"Ai-Gateway-Auth-Method":      "api-key",
		}
		for name, want := range wantHeaders {
			if got := r.Header.Get(name); got != want {
				t.Errorf("header %s: got %q, want %q", name, got, want)
			}
		}
		var body struct {
			State     map[string]string `json:"state"`
			Questions map[string]struct {
				Type         string          `json:"type"`
				Instructions string          `json:"instructions"`
				Criteria     json.RawMessage `json:"criteria"`
			} `json:"questions"`
			ProviderOptions map[string]struct {
				ZeroDataRetention      bool     `json:"zeroDataRetention"`
				DisallowPromptTraining bool     `json:"disallowPromptTraining"`
				Only                   []string `json:"only"`
			} `json:"providerOptions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.State["ticket"] != "Help ASAP" {
			t.Errorf("state: %+v", body.State)
		}
		if body.Questions["urgent"].Type != "boolean" ||
			body.Questions["category"].Type != "choice" ||
			body.Questions["severity"].Type != "score" {
			t.Errorf("questions: %+v", body.Questions)
		}
		var booleanCriteria map[string]string
		if err := json.Unmarshal(body.Questions["urgent"].Criteria, &booleanCriteria); err != nil ||
			booleanCriteria["true"] != "time-sensitive" {
			t.Errorf("boolean criteria: %s, %v", body.Questions["urgent"].Criteria, err)
		}
		gateway := body.ProviderOptions["gateway"]
		if !gateway.ZeroDataRetention || !gateway.DisallowPromptTraining ||
			len(gateway.Only) != 1 || gateway.Only[0] != "typesafe-ai" {
			t.Errorf("gateway options: %+v", gateway)
		}
		w.Header().Set("X-Request-ID", "gateway-request")
		_, _ = io.WriteString(w, `{
			"answers": {
				"urgent": {"type":"boolean","probability":0.99},
				"category": {"type":"choice","choice":"billing","probabilities":{"billing":0.9,"other":0.1}},
				"severity": {"type":"score","score":1.6,"probabilities":{"0":0.1,"1":0.2,"2":0.7}}
			},
			"rounding":{"probabilityDecimals":2,"scoreDecimals":1},
			"usage":{"inputTokens":120,"outputTokens":12},
			"warnings":[{"type":"compatibility","feature":"confidence","details":"metadata only"}],
			"providerMetadata":{"typesafe":{"confidence":{"category":0.8}}}
		}`)
	}))
	defer server.Close()

	client, err := jev.NewClient(jev.Config{
		Provider:            jev.ProviderVercel,
		BaseURL:             server.URL,
		ReadFromEnvironment: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Ask(context.Background(), jev.Request{
		State: map[string]string{"ticket": "Help ASAP"},
		Questions: map[string]jev.Question{
			"urgent": jev.Noul("Is this urgent?", jev.NoulCriteria{
				True: "time-sensitive", False: "not time-sensitive",
			}),
			"category": jev.Choice("Which team?", map[string]any{
				"billing": nil, "other": nil,
			}),
			"severity": jev.Score("How severe?", []string{"low", "medium", "high"}),
		},
		Gateway: &jev.GatewayOptions{
			ZeroDataRetention:      true,
			DisallowPromptTraining: true,
			Only:                   []string{"typesafe-ai"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "typesafe-ai/jev" || result.RequestID != "gateway-request" {
		t.Errorf("metadata: %+v", result)
	}
	if result.Answers["urgent"].Type != jev.QuestionNoul || result.Answers["urgent"].Noul != 0.99 {
		t.Errorf("boolean normalization: %+v", result.Answers["urgent"])
	}
	if result.Answers["category"].Choice != "billing" ||
		!result.Answers["category"].HasConfidence ||
		result.Answers["category"].Confidence != 0.8 {
		t.Errorf("choice: %+v", result.Answers["category"])
	}
	if result.Answers["severity"].Score != 1.6 ||
		string(result.Answers["severity"].Legend["2"]) != `"high"` {
		t.Errorf("score: %+v", result.Answers["severity"])
	}
	if result.Usage.InputTokens != 120 || result.Usage.OutputTokens != 12 ||
		result.Rounding == nil || *result.Rounding.ScoreDecimals != 1 ||
		len(result.Warnings) != 1 || len(result.ProviderMetadata["typesafe"]) == 0 {
		t.Errorf("gateway metadata: %+v", result)
	}
}

func TestVercelProviderRejectsMalformedAnswers(t *testing.T) {
	tests := []struct {
		name     string
		question jev.Question
		answer   string
	}{
		{"wrong boolean type", jev.Noul("x"), `{"type":"noul","probability":0.5}`},
		{"missing boolean probability", jev.Noul("x"), `{"type":"boolean"}`},
		{
			"choice outside probabilities",
			jev.Choice("x", map[string]string{"a": "a"}),
			`{"type":"choice","choice":"b","probabilities":{"a":1}}`,
		},
		{"score outside levels", jev.Score("x", []string{"a", "b"}), `{"type":"score","score":2}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, `{"answers":{"x":`+test.answer+`}}`)
				}),
			)
			defer server.Close()
			client, err := jev.NewClient(jev.Config{
				Provider: jev.ProviderVercel,
				APIKey:   "test",
				BaseURL:  server.URL,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Ask(context.Background(), jev.Request{
				Questions: map[string]jev.Question{"x": test.question},
			}); err == nil {
				t.Fatal("expected malformed answer to fail")
			}
		})
	}
}

func TestVercelProviderAcceptsOptionalProbabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"answers":{
			"choice":{"type":"choice","choice":"a"},
			"score":{"type":"score","score":0}
		}}`)
	}))
	defer server.Close()
	client, err := jev.NewClient(jev.Config{
		Provider: jev.ProviderVercel,
		APIKey:   "test",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Ask(context.Background(), jev.Request{
		Questions: map[string]jev.Question{
			"choice": jev.Choice("choose", map[string]string{"a": "a", "b": "b"}),
			"score":  jev.Score("score", []string{"low", "high"}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answers["choice"].Probabilities != nil ||
		result.Answers["score"].Probabilities != nil ||
		string(result.Answers["score"].Legend["0"]) != `"low"` {
		t.Fatalf("optional response fields: %+v", result.Answers)
	}
}

func TestVercelListModelsIsExplicitlyUnsupported(t *testing.T) {
	client, err := jev.NewClient(jev.Config{Provider: jev.ProviderVercel, APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListModels(context.Background()); err == nil {
		t.Fatal("expected ListModels to reject the provider-specific response shape")
	}
}
