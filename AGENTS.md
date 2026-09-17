# jev-go consumer guide for coding agents

This file is for coding agents integrating `github.com/stumble/jev-go` into a
different Go application. It is a consumer integration contract, not a guide
for contributing to the SDK repository. Do not modify or copy SDK internals
into the consuming application unless the user explicitly asks to fork it.

When an agent does not have this repository checked out, give it this URL:

`https://raw.githubusercontent.com/stumble/jev-go/refs/heads/main/AGENTS.md`

## First load TypeSafe's official skill

This guide supplements, but does not replace, TypeSafe's official `typesafe-ai`
skill. Before designing Jev questions, thresholds, or workflow composition, use
the official skill. If installation is authorized, choose one method:

```bash
# Claude Code
claude plugin marketplace add typesafe-ai/skills
claude plugin install typesafe@typesafe-ai

# Codex and other skill-compatible agents
npx skills add typesafe-ai/skills --skill typesafe-ai
```

Do not install globally unless the user asks. If installation is unavailable or
would mutate the consumer project without authorization, read the official
skill directly:

`https://raw.githubusercontent.com/typesafe-ai/skills/main/skills/typesafe-ai/SKILL.md`

The official skill and live TypeSafe docs own Jev/System One concepts,
judgment design, patterns, and current service behavior. This consumer guide
owns only the `jev-go` adapter details. Start live documentation discovery at
`https://docs.typesafe.ai/llms.txt` and read the relevant State, Primitives,
Confidence, API, pattern, and cookbook pages before implementing a new workflow.

An installable `jev-go` adapter skill is also available from this repository:

```bash
npx skills add Stumble/jev-go --skill jev-go
```

## What this project is

`jev-go` is a dependency-free Go SDK and interactive CLI for TypeSafe AI's Jev
evaluation model. Jev evaluates shared state against named, typed questions and
returns probabilities and structured judgments rather than generated prose.

Jev is not a text generator or an autonomous agent. Code must retain control
of deterministic rules, calculations, side effects, and workflow progression;
Jev supplies narrow semantic judgments over relevant text state. Ask atomic
questions, send independent questions together, and use a second request only
when a previous answer is truly needed to obtain evidence or construct the
next state/options.

The SDK supports two upstream transports behind one public question/answer API:

| Provider | `Config.Provider` | Default model | Credential |
| --- | --- | --- | --- |
| TypeSafe direct | omitted or `jev.ProviderTypeSafe` | `jev-latest` | TypeSafe API key |
| Vercel AI Gateway | `jev.ProviderVercel` | `typesafe-ai/jev` | Vercel AI Gateway API key |

Do not treat the two transports as interchangeable URLs. They use different
endpoints, headers, question type names, response shapes, and credentials. The
SDK owns that translation.

## Integration rules

Follow these rules unless the user explicitly asks for different behavior:

1. Add and import `github.com/stumble/jev-go`; do not reimplement its HTTP
   transport or vendor selected source files into the consumer repository.
2. Choose the provider explicitly when using Vercel. Omitting `Provider`
   intentionally selects TypeSafe direct.
3. Read secrets in the application or deployment layer and pass `APIKey`
   explicitly. The SDK does not read environment variables by default.
4. Never send a Vercel key to `api.typesafe.ai`, or a TypeSafe key to Vercel.
5. Reuse one `Client`; it and its `http.Client` are safe for concurrent calls.
6. Pass a non-nil `context.Context`. Use a context deadline to bound the whole
   operation, including attempts and retry backoff.
7. Use the `Noul`, `Choice`, and `Score` helpers instead of manually building
   `Question` values unless a low-level use case requires it.
8. Check `Answer.Type` before reading type-specific fields when question types
   are selected dynamically.
9. Use `HasConfidence` before acting on `Confidence`. A confidence of zero is
   valid and differs from confidence being unavailable.
10. Calibrate thresholds on labeled application data. Typed output guarantees
    shape, not correctness.
11. Keep deterministic policy, thresholds, side effects, and escalation logic
    in ordinary Go code. Use Jev only for the semantic judgment.
12. Never log API keys, Authorization headers, or complete sensitive state.

## Install

Library:

```bash
go get github.com/stumble/jev-go
```

Interactive CLI:

```bash
go install github.com/stumble/jev-go/cmd/jev@latest
```

The module requires Go 1.22 or newer and has no runtime dependencies outside
the standard library.

## Minimal TypeSafe direct integration

```go
package triage

import (
	"context"
	"fmt"

	jev "github.com/stumble/jev-go"
)

func Classify(ctx context.Context, apiKey, ticket string) error {
	client, err := jev.NewClient(jev.Config{
		APIKey: apiKey,
	})
	if err != nil {
		return err
	}

	result, err := client.Ask(ctx, jev.Request{
		State: map[string]any{"ticket": ticket},
		Questions: map[string]jev.Question{
			"urgent": jev.Noul("Does this ticket require urgent attention?"),
			"team": jev.Choice("Which team owns this ticket?", map[string]string{
				"billing":   "charges, invoices, refunds, and payments",
				"technical": "bugs, outages, and integrations",
				"other":     "none of the other options",
			}),
			"severity": jev.Score("How severe is the customer impact?", []string{
				"low: inconvenience only",
				"medium: degraded workflow with a workaround",
				"high: blocked or losing money",
			}),
		},
	})
	if err != nil {
		return err
	}

	fmt.Println(result.Answers["urgent"].Noul)
	fmt.Println(result.Answers["team"].Choice)
	fmt.Println(result.Answers["severity"].Score)
	return nil
}
```

`SystemOne` is the canonical method name matching TypeSafe terminology. `Ask`
is a convenience alias with identical behavior.

## Minimal Vercel AI Gateway integration

```go
client, err := jev.NewClient(jev.Config{
	Provider: jev.ProviderVercel,
	APIKey:   gatewayKey,
})
if err != nil {
	return err
}

result, err := client.Ask(ctx, jev.Request{
	State: ticket,
	Questions: map[string]jev.Question{
		"urgent": jev.Noul("Does this ticket require urgent attention?"),
	},
	Gateway: &jev.GatewayOptions{
		ZeroDataRetention:      true,
		DisallowPromptTraining: true,
	},
})
```

Supported Gateway controls are `ZeroDataRetention`,
`DisallowPromptTraining`, `Only`, `Order`, `Tags`, and `User`.

The Vercel evaluation contract calls yes/no questions `boolean`; the SDK maps
them to and from the public `QuestionNoul`/`Answer.Noul` representation. It also
reconstructs Score legends from the request and copies TypeSafe confidence from
Gateway provider metadata into the matching answer when present. Original
provider metadata remains in `Response.ProviderMetadata`.

Vercel Evaluation v4 is an experimental transport. Keep `jev-go` current and
do not copy its private wire structs into consuming applications.

## State and question types

`Request.State`, question instructions, and criteria accept JSON-compatible Go
values: strings, structs, maps with string keys, slices, arrays, numbers,
booleans, and nil. `json.Marshal` must be able to encode the complete request.

Use structured state when field names make relationships clearer:

```go
State: map[string]any{
	"ticket": ticket,
	"account": map[string]any{
		"plan": "pro",
		"region": "eu",
	},
}
```

Question semantics:

- `Noul(instructions, optionalCriteria)` returns `P(true)` in `Answer.Noul`.
  Do not interpret it as confidence. Values near 0 mean no; values near 1 mean
  yes; values near 0.5 are uncertain.
- `Choice(instructions, options)` selects an unordered named option. It returns
  `Choice`, optional `Probabilities`, and optional `Confidence`. Include an
  `other` option when the choices may not exhaust the state.
- `Score(instructions, levels)` evaluates an ordered rubric. Levels start at
  zero. `Score` can be fractional, `Probabilities` is keyed by level index, and
  `Legend` maps those indices back to criteria. Use two to ten levels.

Limits enforced before network I/O:

- At least one named question per request.
- Question IDs and Choice option labels cannot be blank.
- Choice requires 1–255 options.
- Score requires 2–10 ordered levels.

Ask independent questions in one request. They share state and Jev evaluates
them in parallel. Do not split one logical evaluation into multiple calls
unless later questions genuinely depend on earlier results.

## Reading answers safely

Answers use the same IDs as questions:

```go
answer := result.Answers["team"]
if answer.Type != jev.QuestionChoice {
	return fmt.Errorf("unexpected answer type %q", answer.Type)
}

if answer.HasConfidence && answer.Confidence < 0.4 {
	return routeToHumanReview(ticket)
}
return assignTeam(answer.Choice)
```

Provider differences are normalized where possible:

| Field | TypeSafe direct | Vercel Gateway |
| --- | --- | --- |
| `Noul` | Always present for Noul | Mapped from Gateway boolean probability |
| `Choice` / `Score` | Present for matching type | Present for matching type |
| `Probabilities` | Required by direct API | Optional in Gateway contract |
| `Legend` | Returned by direct API | Reconstructed from request |
| `Confidence` | Returned by direct API | Populated from metadata when available |
| `HasConfidence` | True for Choice/Score | True only when metadata supplied it |
| `Rounding` | Usually nil | May describe Gateway output precision |

The SDK rejects malformed or inconsistent responses, including:

- Missing, extra, or type-mismatched answer IDs.
- Choice values outside the requested options.
- Score values outside the requested rubric.
- Incomplete or unexpected probability/legend keys.
- Probability values outside `[0,1]` or distributions that do not sum to 1,
  allowing for declared rounding precision.
- Direct Score legends that do not semantically match the requested criteria.

Do not add a second layer of JSON shape parsing around these answers.

## Configuration

Explicit application configuration is preferred:

```go
client, err := jev.NewClient(jev.Config{
	Provider:     jev.ProviderTypeSafe,
	APIKey:       cfg.TypeSafeAPIKey,
	DefaultModel: "jev-latest",
	Timeout:      10 * time.Second, // per attempt
	Retry:        &jev.RetryPolicy{MaxRetries: 2},
	HTTPClient:   sharedHTTPClient,
})
```

Environment lookup is opt-in. Only use it when that is the application's
intended configuration boundary:

```go
client, err := jev.NewClient(jev.Config{
	Provider:            jev.ProviderVercel,
	ReadFromEnvironment: true,
})
```

Environment fallbacks:

- TypeSafe: `TYPESAFE_API_KEY`, `TYPESAFE_BASE_URL`,
  `TYPESAFE_DEFAULT_MODEL`.
- Vercel: `AI_GATEWAY_API_KEY` only.

Explicit nonblank values win over environment values. Known credential/host
mismatches are rejected. Remote custom endpoints must use HTTPS; loopback HTTP
is allowed for tests. Redirect following is disabled so bearer credentials are
not forwarded to another endpoint.

The client owns Authorization, content negotiation, provider protocol, model,
and retry-count headers. Do not attempt to replace them through `Headers`.

## Timeouts and retries

`Config.Timeout` and `CallOptions.Timeout` are per-attempt timeouts. A context
deadline is the total caller-controlled budget:

```go
ctx, cancel := context.WithTimeout(parent, 30*time.Second)
defer cancel()

result, err := client.Ask(ctx, request, jev.CallOptions{
	Timeout: 8 * time.Second,
})
```

The default policy performs up to two retries after the initial attempt for
connection failures, per-attempt timeouts, HTTP 408, HTTP 429, and HTTP 5xx.
It uses jittered exponential backoff and honors bounded `Retry-After` and
`Retry-After-Ms` values. Set `Retry: &jev.RetryPolicy{MaxRetries: 0}` to disable
retries. Context cancellation interrupts both HTTP attempts and backoff.

## Error handling

Never parse error strings. Use `errors.As` for HTTP errors and `errors.Is` for
context cancellation:

```go
result, err := client.Ask(ctx, request)
if err != nil {
	var apiErr *jev.APIError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &apiErr):
		log.Printf("evaluation failed: status=%d request_id=%s code=%s",
			apiErr.StatusCode, apiErr.RequestID, apiErr.Code)
		return err
	default:
		return err
	}
}
_ = result
```

`APIError.Body` contains a bounded raw error response for diagnostics. Treat it
as potentially sensitive and do not log it indiscriminately.

## Map ordering and concurrency

Questions and Choice options are maps because the protocol addresses them by
name; their order has no semantic meaning. The current standard-library
`encoding/json` implementation sorts map keys during encoding, and validation
sorts question IDs before returning an error. Score levels use slices because
their order is semantic.

Do not mutate request maps, slices, structs, `Config.Headers`, or
`CallOptions.Headers` concurrently with a call. Build the request completely,
then submit it. Concurrent calls that use independent requests are supported.

## CLI

The CLI is a safe way to explore Jev manually:

```bash
TYPESAFE_API_KEY=... jev
AI_GATEWAY_API_KEY=... jev -provider vercel
```

Useful Vercel flags:

```bash
jev -provider vercel -zero-data-retention -no-training
```

Prompts go to stderr and normalized JSON goes to stdout, so this works:

```bash
AI_GATEWAY_API_KEY=... jev -provider vercel | jq
```

Provider metadata is hidden by default. Add `-show-metadata` for routing, cost,
and generation information. The CLI intentionally has no API-key flag; keys
must not be put in shell history or process arguments.

## Testing an integration

Do not use paid or live APIs in ordinary unit tests. Supply a loopback
`BaseURL` backed by `httptest.Server`, assert request headers/body, and return a
representative provider response. Test at least:

- The selected provider and model.
- Each question type used by the application.
- Low-confidence or uncertain business behavior.
- Context cancellation and application timeout behavior.
- An `APIError` path.
- That no secret appears in logs or returned errors.

For an explicit live smoke test, use the installed CLI rather than embedding a
credential in a test:

```bash
AI_GATEWAY_API_KEY=... go run github.com/stumble/jev-go/cmd/jev@latest \
  -provider vercel
```

Never add a live credential to source, fixtures, command arguments, snapshots,
or CI configuration.

## Consumer delivery checklist

Before reporting a `jev-go` integration complete, verify all applicable items:

- `go.mod` contains `github.com/stumble/jev-go` and `go mod tidy` is clean.
- The intended provider is explicit; Vercel integrations set
  `ProviderVercel`, while TypeSafe direct intentionally uses the default or
  `ProviderTypeSafe`.
- The application reads the correct credential from its own configuration
  layer and passes `APIKey` explicitly, or deliberately opts into
  `ReadFromEnvironment`.
- No key appears in source, fixtures, command arguments, logs, snapshots, or
  error messages.
- Every production call receives a non-nil context with an appropriate total
  deadline.
- `APIError` and context errors are handled without parsing strings.
- Low-confidence and uncertain answers have an explicit review, fallback, or
  abstention path.
- Unit tests use `httptest.Server`, cover every question type used, and do not
  spend money or require network access.
- The application's normal formatter, linter, tests, race tests, and build all
  pass.
- If authority and a temporary credential are available, one bounded live
  smoke test succeeds through the selected provider.

If a task requires changing SDK behavior rather than consuming the released
API, stop and make that scope expansion explicit. Do not silently patch around
the SDK in the consumer application.

## Authoritative links

- SDK repository and human README:
  `https://github.com/Stumble/jev-go`
- Raw agent guide:
  `https://raw.githubusercontent.com/stumble/jev-go/refs/heads/main/AGENTS.md`
- Official TypeSafe skill documentation:
  `https://docs.typesafe.ai/agent-skill`
- Raw official TypeSafe skill:
  `https://raw.githubusercontent.com/typesafe-ai/skills/main/skills/typesafe-ai/SKILL.md`
- Raw jev-go adapter skill:
  `https://raw.githubusercontent.com/stumble/jev-go/refs/heads/main/skills/jev-go/SKILL.md`
- TypeSafe JavaScript SDK behavior:
  `https://docs.typesafe.ai/sdk/javascript`
- TypeSafe HTTP API:
  `https://docs.typesafe.ai/api`
- TypeSafe model list:
  `https://docs.typesafe.ai/models`
- Vercel Jev model page:
  `https://vercel.com/ai-gateway/models/jev`
- Vercel Evaluation documentation:
  `https://vercel.com/docs/ai-gateway/modalities/evaluation`
