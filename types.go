// Package jev provides a Go client for TypeSafe AI's System One API.
// It evaluates named Noul, Choice, and Score questions about JSON-compatible state.
package jev

import "encoding/json"

// QuestionType identifies the kind of answer a question requests.
type QuestionType string

const (
	QuestionNoul   QuestionType = "noul"
	QuestionChoice QuestionType = "choice"
	QuestionScore  QuestionType = "score"
)

// Question describes a judgment to make about the request's state.
// Instructions and Criteria may contain strings, objects, arrays, or nil.
// Prefer the Noul, Choice, and Score helpers for common questions.
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions,omitempty"`
	Criteria     any          `json:"criteria,omitempty"`
}

// NoulCriteria optionally describes the yes and no outcomes.
type NoulCriteria struct {
	True  any `json:"true,omitempty"`
	False any `json:"false,omitempty"`
}

// Noul creates a yes/no question. Its answer's Noul field is P(yes).
func Noul(instructions any, criteria ...NoulCriteria) Question {
	q := Question{Type: QuestionNoul, Instructions: instructions}
	if len(criteria) > 0 {
		q.Criteria = criteria[0]
	}
	return q
}

// Choice creates a question selecting one of the named options.
// Descriptions may be nil when a label needs no additional explanation.
func Choice[T any](instructions any, criteria map[string]T) Question {
	options := make(map[string]any, len(criteria))
	for label, description := range criteria {
		options[label] = description
	}
	return Question{Type: QuestionChoice, Instructions: instructions, Criteria: options}
}

// Score creates a question rating state against ordered levels, starting at zero.
func Score[T any](instructions any, criteria []T) Question {
	levels := make([]any, len(criteria))
	for i, description := range criteria {
		levels[i] = description
	}
	return Question{Type: QuestionScore, Instructions: instructions, Criteria: levels}
}

// Request evaluates one or more named questions about JSON-compatible state.
// Set Model to override the client's default model for this call.
type Request struct {
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
	Model     string              `json:"model,omitempty"`
}

// Answer is discriminated by Type. Read only the fields for that type:
// Noul for "noul"; Choice, Probabilities, and Confidence for "choice";
// Score, Legend, Probabilities, and Confidence for "score".
type Answer struct {
	Type          QuestionType               `json:"type"`
	Noul          float64                    `json:"noul,omitempty"`
	Choice        string                     `json:"choice,omitempty"`
	Score         float64                    `json:"score,omitempty"`
	Confidence    float64                    `json:"confidence,omitempty"`
	Probabilities map[string]float64         `json:"probabilities,omitempty"`
	Legend        map[string]json.RawMessage `json:"legend,omitempty"`
}

// Usage reports the API's token accounting.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response contains the answers under the same keys as the request's questions.
type Response struct {
	Model     string            `json:"model"`
	Answers   map[string]Answer `json:"answers"`
	Usage     Usage             `json:"usage"`
	RequestID string            `json:"-"` // from the HTTP response header, if provided
}

// Model describes an available model or alias.
type Model struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}
