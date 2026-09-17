---
name: jev-go
license: MIT
description: >
  Integrate github.com/stumble/jev-go into a consuming Go application after
  using TypeSafe's official skill for Jev and System One design. Use when
  adding TypeSafe-direct or Vercel Gateway evaluations, translating Choice,
  Score, and Noul judgments into Go control flow, configuring credentials,
  handling uncertainty and errors, or testing the integration. Do not use for
  modifying the jev-go SDK itself.
---

# Integrate jev-go

Use this skill for the Go adapter layer around Jev. TypeSafe's official skill
owns the model concepts, judgment design, patterns, and current service
guidance; this skill owns the idiomatic use of `jev-go` in a consumer.

## Load the official TypeSafe skill first

Before designing questions or thresholds, use the official `typesafe-ai` skill.
If it is already installed, invoke it by name. If installation is authorized,
use exactly one official installation method:

```bash
# Claude Code
claude plugin marketplace add typesafe-ai/skills
claude plugin install typesafe@typesafe-ai

# Codex and other skill-compatible agents
npx skills add typesafe-ai/skills --skill typesafe-ai
```

Do not install globally unless the user asks. If installation is unavailable or
would mutate the consumer repository without authorization, read the official
skill directly instead:

`https://raw.githubusercontent.com/typesafe-ai/skills/main/skills/typesafe-ai/SKILL.md`

Then read the relevant live TypeSafe documentation. Start from
`https://docs.typesafe.ai/llms.txt`; at minimum inspect the current State,
Primitives, chosen primitive, Confidence, and API pages. For a new workflow,
find the closest current pattern or cookbook. Live docs override cached skill
knowledge and examples.

## Preserve the Jev programming model

- Jev is a System One evaluation model, not a text generator or autonomous
  agent. It returns typed judgments and probabilities; it does not write a
  response, produce reasoning, or own a workflow.
- Keep deterministic rules, calculations, data access, side effects, and
  control flow in Go. Use Jev only where semantic understanding is required.
- Jev currently evaluates text: a string, or a JSON object/array containing
  text and related application facts. Do not send images, audio, or video.
- Ask narrow judgments a knowledgeable person could make quickly from the
  supplied state. Split broad judgments into independently useful dimensions.
- Put related named facts in structured state. Refer to nested fields in
  instructions using explicit backticked paths when that removes ambiguity.
- Question IDs are only response keys; the model does not see them. Put the
  complete meaning in instructions and criteria.
- Ask independent questions over the same state together, including useful
  speculative branch questions. Use a second request only when an earlier
  answer is genuinely needed to fetch evidence, create new state, or choose
  later options.
- Choice is for one unordered option, Score for an ordered degree, and Noul for
  `P(true)`. A Noul near 0.5 is uncertainty, not medium intensity.
- Choice/Score confidence summarizes distribution concentration. It is not a
  correctness guarantee. Keep raw probabilities available and calibrate every
  threshold on representative domain data and action risk.
- Include a no-match Choice option when candidate coverage is not guaranteed.
  The model cannot select an omitted value.
- Keep questions, criteria, weights, and threshold constants together so a
  human can review and tune the actual decision boundary.

## Apply the Go SDK boundary

Install and import the module rather than copying its transport:

```bash
go get github.com/stumble/jev-go
```

```go
import jev "github.com/stumble/jev-go"
```

Choose one provider:

- TypeSafe direct is the zero-value default: omit `Provider` or use
  `jev.ProviderTypeSafe`. Its default model is `jev-latest`.
- Vercel AI Gateway requires `jev.ProviderVercel`. Its default model is
  `typesafe-ai/jev`.

Read credentials in the consuming application's configuration layer and pass
`Config.APIKey` explicitly. The client intentionally does not read environment
variables unless `ReadFromEnvironment` is true. Never send one provider's key
to the other provider.

Create and reuse one client. Pass a non-nil context and use a context deadline
as the total budget across attempts and backoff. Prefer the `Noul`, `Choice`,
and `Score` constructors. Send one `Request` containing all independent
questions. `Ask` and `SystemOne` have identical behavior.

For Vercel-only controls, set `Request.Gateway`. Available typed controls
include zero-data-retention, disallow-prompt-training, provider allow/order,
tags, and user attribution. Do not put Gateway options on a TypeSafe-direct
request.

## Consume normalized answers correctly

The SDK normalizes Vercel `boolean` answers to `QuestionNoul` and `Answer.Noul`.
It reconstructs Vercel Score legends and imports TypeSafe confidence from
provider metadata when supplied.

- Read `Noul` only for `QuestionNoul`.
- Read `Choice`, `Probabilities`, and optional confidence for
  `QuestionChoice`.
- Read `Score`, `Legend`, `Probabilities`, and optional confidence for
  `QuestionScore`.
- When question types are dynamic, branch on `Answer.Type` first.
- Check `HasConfidence` before using `Confidence`; zero is a valid confidence
  value and is different from absence.
- Do not assume Gateway probabilities or confidence are always present.
- Preserve an explicit fallback, clarification, human-review, or reasoning
  path for uncertainty proportional to the consequence of a wrong action.

Use `errors.As` with `*jev.APIError` for non-success HTTP responses and
`errors.Is` for context cancellation/deadlines. Do not parse error strings or
log `APIError.Body` without considering sensitive content.

## Test the consumer behavior

Use `httptest.Server` and a loopback `BaseURL` in ordinary tests. Do not require
a live key, spend money, or depend on provider availability in CI. Assert the
provider-specific request shape only at the boundary, and test the application
behavior produced by normalized answers.

Cover:

- Every question type the application uses.
- Provider and model selection.
- Confident, uncertain, and malformed/error paths.
- Context deadline and cancellation behavior.
- `APIError` handling.
- The deterministic action, review, or fallback selected by application code.
- Absence of keys and sensitive state from logs and snapshots.

Use the CLI for an authorized, bounded live smoke test:

```bash
AI_GATEWAY_API_KEY=... go run github.com/stumble/jev-go/cmd/jev@latest \
  -provider vercel
```

## Detailed jev-go reference

For complete Go examples, provider differences, field semantics, retries,
security boundaries, map ordering, CLI behavior, and a delivery checklist, read:

`https://raw.githubusercontent.com/stumble/jev-go/refs/heads/main/AGENTS.md`

Treat that consumer guide as the `jev-go` adapter reference and the official
TypeSafe skill plus live docs as the Jev design authority. If they conflict on
Jev concepts or current service behavior, follow TypeSafe. If the task requires
changing SDK behavior rather than consuming it, stop and make that scope change
explicit instead of patching around the client.
