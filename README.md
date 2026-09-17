# jev-go

An independent Go SDK for [TypeSafe AI's Jev / System One API](https://docs.typesafe.ai/api). This is a community-maintained client, not an official TypeSafe AI SDK.

## For coding agents

Give an agent this single, self-contained integration guide:

`https://raw.githubusercontent.com/Stumble/jev-go/main/AGENTS.md`

The rendered version is [AGENTS.md](https://github.com/Stumble/jev-go/blob/main/AGENTS.md). It covers provider selection, complete request/response semantics, security boundaries, retries, error handling, CLI usage, testing, and repository contribution rules.

## Install

```sh
go get github.com/stumble/jev-go
```

Provide `TYPESAFE_API_KEY` for the direct provider or `AI_GATEWAY_API_KEY` for Vercel through your server-side configuration (never embed keys in a browser or commit them to a repository).

To install the interactive CLI:

```sh
go install github.com/stumble/jev-go/cmd/jev@latest
```

Run it with either provider. The CLI reads the credential itself and passes it explicitly to the SDK:

```sh
# TypeSafe direct API (the default provider)
TYPESAFE_API_KEY=... jev

# Vercel AI Gateway
AI_GATEWAY_API_KEY=... jev -provider vercel \
  -zero-data-retention -no-training
```

The wizard accepts text or a one-line JSON object/array as state, then lets you add any mix of Noul, Choice, and Score questions. Submit a blank question ID to send the request; prompts go to stderr and the normalized response goes to stdout as indented JSON, so piping into `jq` works. Provider metadata is hidden by default because Gateway routing data is verbose; add `-show-metadata` when you need cost, routing, or generation details. Other options include `-model`, `-timeout`, and `-base-url` (useful for a proxy or local testing). Run `jev -h` for the complete list. API keys are intentionally not accepted as command-line flags so they do not enter shell history or process listings.

## Ask questions

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	jev "github.com/stumble/jev-go"
)

func main() {
	client, err := jev.NewClient(jev.Config{APIKey: os.Getenv("TYPESAFE_API_KEY")})
	if err != nil {
		log.Fatal(err)
	}

	response, err := client.Ask(context.Background(), jev.Request{
		State: map[string]any{"document": "I was charged twice. Please fix this ASAP."},
		Questions: map[string]jev.Question{
			"category": jev.Choice("What is this ticket about?", map[string]any{
				"billing":   nil,
				"technical": nil,
				"other":     nil,
			}),
			"urgent": jev.Noul("Does this ticket convey urgency?"),
			"priority": jev.Score("How urgent is this ticket?", []string{
				"Can wait", "Today", "Right now",
			}),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(response.Answers["category"].Choice)
	fmt.Println(response.Answers["urgent"].Noul) // probability of yes
	fmt.Println(response.Answers["priority"].Score) // weighted score, possibly fractional
}
```

`SystemOne(ctx, request)` is the canonical method matching the JavaScript SDK's `systemOne`; `Ask` is an alias. Requests can mix any number of named Noul, Choice, and Score questions. Answers are keyed by those names; check an answer's `Type` before interpreting its type-specific fields. Missing or malformed answer fields are reported as errors rather than mistaken for a valid zero. `Response` also includes the model, token usage, and an optional HTTP request ID. State, instructions, and criteria accept JSON-compatible structured values. `ListModels(ctx)` returns the models available to the account.

Named questions and Choice options use maps because the HTTP protocol uses JSON objects keyed by name. Go's `encoding/json` sorts map keys when encoding, and their order has no meaning for these questions. Score levels use a slice because their order defines the rubric. Invalid questions are checked in sorted name order, so the first validation error is repeatable. Don't mutate input maps concurrently with a request.

The JavaScript SDK infers answer types at compile time from its TypeScript question map. Go does not infer heterogeneous map values that way, so this client uses an explicit answer discriminator and ordinary Go fields instead. For structured Score legends, `Answer.Legend` uses `json.RawMessage` to preserve each level's JSON value.

## Configuration and errors

```go
client, err := jev.NewClient(jev.Config{
	APIKey:       "...", // supplied by your application
	DefaultModel: "jev-latest",
	Timeout:      10 * time.Second, // per attempt
	Retry:        &jev.RetryPolicy{MaxRetries: 2},
})

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

result, err := client.SystemOne(ctx, request, jev.CallOptions{
	Timeout: 5 * time.Second,
})
var apiErr *jev.APIError
if errors.As(err, &apiErr) {
	log.Printf("TypeSafe returned HTTP %d, request %s: %s", apiErr.StatusCode, apiErr.RequestID, apiErr.Message)
}
_ = result
```

By default the SDK never reads environment variables. Pass settings from your application's configuration, or explicitly set `ReadFromEnvironment: true`. The TypeSafe provider then uses `TYPESAFE_API_KEY`, `TYPESAFE_BASE_URL`, and `TYPESAFE_DEFAULT_MODEL` as fallbacks; the Vercel provider uses `AI_GATEWAY_API_KEY`. Explicit values always win. A custom `*http.Client` and additional request headers can be supplied through `Config`; per-call headers and timeout can be supplied through `CallOptions`. The client owns authentication and protocol headers and does not follow redirects with the bearer token. Non-HTTPS custom endpoints are allowed only on loopback for safe local testing.

The default retry policy makes up to two retries for connection failures, per-attempt timeouts, and HTTP 408, 429, and 5xx responses (including 529). It uses jittered exponential backoff and honors `Retry-After` and `Retry-After-Ms` up to one minute. Set `Retry: &jev.RetryPolicy{MaxRetries: 0}` to disable retries. Cancellation of the caller's context interrupts both requests and pending backoff; use a context deadline to bound the whole operation.

## Vercel AI Gateway

Select `ProviderVercel` to call [Jev on Vercel AI Gateway](https://vercel.com/ai-gateway/models/jev). The client uses Gateway's Evaluation v4 transport and normalizes its `boolean` answers to the same `Noul` answer exposed by the TypeSafe provider.

```go
client, err := jev.NewClient(jev.Config{
	Provider: jev.ProviderVercel,
	APIKey:   os.Getenv("AI_GATEWAY_API_KEY"),
})

response, err := client.Ask(ctx, jev.Request{
	State: "The support agent issued a full refund.",
	Questions: map[string]jev.Question{
		"refunded": jev.Noul("Was a refund issued?"),
	},
	Gateway: &jev.GatewayOptions{
		ZeroDataRetention:      true,
		DisallowPromptTraining: true,
	},
})
```

The Vercel defaults are `https://ai-gateway.vercel.sh/v4/ai` and `typesafe-ai/jev`. With `ReadFromEnvironment: true`, this provider reads only `AI_GATEWAY_API_KEY`; the application can still pass the key explicitly as above. The client reconstructs Score legends from the request and normalizes TypeSafe's Gateway confidence metadata into `Answer.Confidence`; `HasConfidence` distinguishes a missing value from a valid zero. Original metadata remains available in `Response.ProviderMetadata`. `ListModels` currently targets the TypeSafe account-specific model API and returns an explicit error with the Vercel provider.

Vercel documents Evaluation as an AI SDK 7 feature rather than an OpenAI-compatible endpoint. This package follows the public Evaluation v4 contract implemented by Vercel's open-source Gateway provider; changes to that experimental contract may require an SDK update. Never send a Vercel key to the TypeSafe endpoint or vice versa—the client rejects those known mismatches.

## Development

This module has no runtime dependencies. Run `make lint-fix` to apply the Alva-inspired golangci-lint v2 configuration, then `make ci` to check formatting, lint, race tests, and the enforced 85% total statement-coverage floor. CI tests Go 1.22 and stable Go; HTTP contract tests use local servers and do not need an API key or contact TypeSafe AI.

API reference: [JavaScript SDK](https://docs.typesafe.ai/sdk/javascript), [HTTP API](https://docs.typesafe.ai/api), [model listing](https://docs.typesafe.ai/models).
