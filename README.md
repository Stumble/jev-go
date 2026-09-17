# jev-go

An independent Go SDK for [TypeSafe AI's Jev / System One API](https://docs.typesafe.ai/api). This is a community-maintained client, not an official TypeSafe AI SDK.

## Install

```sh
go get github.com/stumble/jev-go
```

Set `TYPESAFE_API_KEY` in your server environment (never embed it in a browser or commit it to a repository).

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

By default the SDK never reads environment variables. Pass the API key and other settings from your application's configuration, or explicitly use `jev.Config{ReadFromEnvironment: true}` to enable `TYPESAFE_API_KEY`, `TYPESAFE_BASE_URL`, and `TYPESAFE_DEFAULT_MODEL` as fallbacks. Explicit values win over environment variables; defaults are `https://api.typesafe.ai` and `jev-latest`. A custom `*http.Client` and additional request headers can be supplied through `Config`; per-call headers and timeout can be supplied through `CallOptions`. The client owns Authorization, Accept, Content-Type, and retry-count headers, and does not follow redirects with the bearer token. Non-HTTPS custom endpoints are allowed only on loopback for safe local testing.

The default retry policy makes up to two retries for connection failures, per-attempt timeouts, and HTTP 408, 429, and 5xx responses (including 529). It uses jittered exponential backoff and honors `Retry-After` and `Retry-After-Ms` up to one minute. Set `Retry: &jev.RetryPolicy{MaxRetries: 0}` to disable retries. Cancellation of the caller's context interrupts both requests and pending backoff; use a context deadline to bound the whole operation.

## Vercel AI Gateway

Vercel lists Jev as an evaluation model under `typesafe-ai/jev`, but [Gateway Evaluation is currently available only through AI SDK 7](https://vercel.com/docs/ai-gateway/modalities/evaluation). It is **not** available through the documented REST-compatible Gateway endpoints, and it uses a different Boolean answer shape from TypeSafe's direct Noul response. Therefore, a Vercel Gateway key or Gateway `BaseURL` cannot be used with this direct TypeSafe client. Do not send a Gateway key to `api.typesafe.ai`. Supporting Gateway evaluation in Go would require Vercel to publish a stable HTTP contract or a separate service running their AI SDK; this package does not rely on private Gateway endpoints.

## Development

This module has no runtime dependencies. Run `make lint-fix` to apply the Alva-inspired golangci-lint v2 configuration, then `make ci` to check formatting, lint and race tests. CI tests Go 1.22 and stable Go; HTTP contract tests use local servers and do not need an API key or contact TypeSafe AI.

API reference: [JavaScript SDK](https://docs.typesafe.ai/sdk/javascript), [HTTP API](https://docs.typesafe.ai/api), [model listing](https://docs.typesafe.ai/models).
