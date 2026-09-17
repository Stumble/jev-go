//go:build integration

package jev_test

import (
	"context"
	"os"
	"testing"
	"time"

	jev "github.com/stumble/jev-go"
)

func TestVercelLive(t *testing.T) {
	key := os.Getenv("AI_GATEWAY_API_KEY")
	if key == "" {
		t.Skip("AI_GATEWAY_API_KEY is not set")
	}
	client, err := jev.NewClient(jev.Config{
		Provider: jev.ProviderVercel,
		APIKey:   key,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := client.Ask(ctx, jev.Request{
		State: "The support agent issued a full refund to the customer.",
		Questions: map[string]jev.Question{
			"refunded": jev.Noul("Was a refund issued?"),
			"route": jev.Choice("Which team owns this?", map[string]string{
				"billing": "payments and refunds",
				"other":   "anything else",
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "typesafe-ai/jev" || len(result.Answers) != 2 {
		t.Fatalf("unexpected result: model=%q, answers=%d", result.Model, len(result.Answers))
	}
	if !result.Answers["route"].HasConfidence {
		t.Fatal("TypeSafe confidence was not normalized from provider metadata")
	}
	t.Logf(
		"Jev answered: refunded=%.3f route=%s confidence=%.3f; usage=%+v",
		result.Answers["refunded"].Noul,
		result.Answers["route"].Choice,
		result.Answers["route"].Confidence,
		result.Usage,
	)
}
