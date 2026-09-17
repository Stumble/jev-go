// Command jev is an interactive terminal client for TypeSafe Jev.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	jev "github.com/stumble/jev-go"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) int {
	flags := flag.NewFlagSet("jev", flag.ContinueOnError)
	flags.SetOutput(stderr)
	providerName := flags.String("provider", "typesafe", "provider: typesafe or vercel")
	model := flags.String("model", "", "override the provider's default model")
	baseURL := flags.String("base-url", "", "override the provider API base URL")
	timeout := flags.Duration("timeout", 30*time.Second, "total request timeout")
	zdr := flags.Bool("zero-data-retention", false, "require Vercel zero-data-retention routing")
	noTraining := flags.Bool("no-training", false, "disallow prompt training through Vercel")
	showMetadata := flags.Bool("show-metadata", false, "include provider metadata in JSON output")
	flags.Usage = func() {
		writeLine(stderr, "Usage: jev [options]")
		writeLine(stderr, "Interactively build and send one Jev evaluation.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		writeLine(stderr, "jev: unexpected positional arguments")
		flags.Usage()
		return 2
	}
	if *timeout <= 0 {
		writeLine(stderr, "jev: timeout must be positive")
		return 2
	}

	provider, keyEnv, err := parseProvider(*providerName)
	if err != nil {
		writeLine(stderr, err)
		return 2
	}
	if provider != jev.ProviderVercel && (*zdr || *noTraining) {
		writeLine(stderr, "jev: Gateway routing options require -provider vercel")
		return 2
	}
	apiKey := strings.TrimSpace(os.Getenv(keyEnv))
	if apiKey == "" {
		writef(stderr, "jev: %s is required for provider %s\n", keyEnv, provider)
		return 2
	}
	client, err := jev.NewClient(jev.Config{
		Provider:     provider,
		APIKey:       apiKey,
		BaseURL:      *baseURL,
		DefaultModel: *model,
	})
	if err != nil {
		writeLine(stderr, err)
		return 2
	}

	// Prompts go to stderr so stdout remains clean JSON for pipes and scripts.
	prompt := newPrompter(stdin, stderr)
	request, err := prompt.request(provider, *zdr, *noTraining)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			writeLine(stderr, "jev:", err)
		} else {
			writeLine(stderr, "jev: input ended before the request was complete")
		}
		return 2
	}
	request.Model = *model
	requestCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	result, err := client.Ask(requestCtx, request)
	if err != nil {
		writeLine(stderr, err)
		return 1
	}
	if !*showMetadata {
		result.ProviderMetadata = nil
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		writeLine(stderr, "jev: write response:", err)
		return 1
	}
	return 0
}

func parseProvider(value string) (jev.Provider, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "typesafe":
		return jev.ProviderTypeSafe, "TYPESAFE_API_KEY", nil
	case "vercel":
		return jev.ProviderVercel, "AI_GATEWAY_API_KEY", nil
	default:
		return "", "", fmt.Errorf("jev: unsupported provider %q (use typesafe or vercel)", value)
	}
}

func writeLine(w io.Writer, values ...any) {
	_, _ = fmt.Fprintln(w, values...)
}

func writef(w io.Writer, format string, values ...any) {
	_, _ = fmt.Fprintf(w, format, values...)
}

type prompter struct {
	scanner *bufio.Scanner
	out     io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	return &prompter{scanner: scanner, out: out}
}

func (p *prompter) request(provider jev.Provider, zdr, noTraining bool) (jev.Request, error) {
	writef(p.out, "Jev interactive client (%s)\n", provider)
	stateText, err := p.read("State (text or one-line JSON object/array): ")
	if err != nil {
		return jev.Request{}, err
	}
	state, err := parseState(stateText)
	if err != nil {
		return jev.Request{}, err
	}
	questions := make(map[string]jev.Question)
	for {
		id, err := p.read("Question ID (blank to send): ")
		if err != nil {
			return jev.Request{}, err
		}
		id = strings.TrimSpace(id)
		if id == "" {
			if len(questions) == 0 {
				writeLine(p.out, "Add at least one question.")
				continue
			}
			break
		}
		if _, exists := questions[id]; exists {
			writef(p.out, "Question %q already exists.\n", id)
			continue
		}
		question, err := p.question()
		if err != nil {
			return jev.Request{}, err
		}
		questions[id] = question
	}
	request := jev.Request{State: state, Questions: questions}
	if provider == jev.ProviderVercel && (zdr || noTraining) {
		request.Gateway = &jev.GatewayOptions{
			ZeroDataRetention:      zdr,
			DisallowPromptTraining: noTraining,
		}
	}
	return request, nil
}

func parseState(value string) (any, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || (!strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[")) {
		return value, nil
	}
	var result any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid state JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("invalid state JSON: provide exactly one value")
		}
		return nil, fmt.Errorf("invalid state JSON: %w", err)
	}
	return result, nil
}

func (p *prompter) question() (jev.Question, error) {
	kind, err := p.read("Type (noul/boolean, choice, score): ")
	if err != nil {
		return jev.Question{}, err
	}
	instructions, err := p.read("Instructions: ")
	if err != nil {
		return jev.Question{}, err
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "noul", "boolean":
		trueDescription, err := p.read("True description (optional): ")
		if err != nil {
			return jev.Question{}, err
		}
		falseDescription, err := p.read("False description (optional): ")
		if err != nil {
			return jev.Question{}, err
		}
		if trueDescription == "" && falseDescription == "" {
			return jev.Noul(instructions), nil
		}
		return jev.Noul(instructions, jev.NoulCriteria{
			True: optionalText(trueDescription), False: optionalText(falseDescription),
		}), nil
	case "choice":
		criteria, err := p.choiceCriteria()
		if err != nil {
			return jev.Question{}, err
		}
		return jev.Choice(instructions, criteria), nil
	case "score":
		criteria, err := p.scoreCriteria()
		if err != nil {
			return jev.Question{}, err
		}
		return jev.Score(instructions, criteria), nil
	default:
		return jev.Question{}, fmt.Errorf("unknown question type %q", kind)
	}
}

func (p *prompter) choiceCriteria() (map[string]any, error) {
	criteria := make(map[string]any)
	for {
		label, err := p.read("Option label (blank when done): ")
		if err != nil {
			return nil, err
		}
		label = strings.TrimSpace(label)
		if label == "" {
			if len(criteria) == 0 {
				writeLine(p.out, "Add at least one option.")
				continue
			}
			return criteria, nil
		}
		if _, exists := criteria[label]; exists {
			writef(p.out, "Option %q already exists.\n", label)
			continue
		}
		description, err := p.read("Option description (optional): ")
		if err != nil {
			return nil, err
		}
		criteria[label] = optionalText(description)
	}
}

func (p *prompter) scoreCriteria() ([]any, error) {
	criteria := make([]any, 0, 4)
	for {
		level, err := p.read(fmt.Sprintf("Level %d (blank when done): ", len(criteria)))
		if err != nil {
			return nil, err
		}
		if level == "" {
			if len(criteria) < 2 {
				writeLine(p.out, "Add at least two levels.")
				continue
			}
			return criteria, nil
		}
		criteria = append(criteria, level)
	}
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (p *prompter) read(label string) (string, error) {
	if _, err := fmt.Fprint(p.out, label); err != nil {
		return "", err
	}
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return p.scanner.Text(), nil
}
