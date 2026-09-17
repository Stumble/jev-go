// Package jev evaluates named Noul, Choice, and Score questions with TypeSafe
// AI's Jev, either directly or through Vercel AI Gateway.
package jev

import (
	"encoding/json"
	"errors"
)

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
	// Gateway contains request options used only with ProviderVercel.
	Gateway *GatewayOptions `json:"-"`
}

// GatewayOptions contains the Vercel AI Gateway evaluation controls most
// relevant to Jev. A nil value sends no providerOptions field.
type GatewayOptions struct {
	ZeroDataRetention      bool     `json:"zeroDataRetention,omitempty"`
	DisallowPromptTraining bool     `json:"disallowPromptTraining,omitempty"`
	Only                   []string `json:"only,omitempty"`
	Order                  []string `json:"order,omitempty"`
	Tags                   []string `json:"tags,omitempty"`
	User                   string   `json:"user,omitempty"`
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
	HasConfidence bool                       `json:"-"` // false when the provider did not supply confidence
	Probabilities map[string]float64         `json:"probabilities,omitempty"`
	Legend        map[string]json.RawMessage `json:"legend,omitempty"`
}

// MarshalJSON includes zero-valued results, which are meaningful answers.
func (a Answer) MarshalJSON() ([]byte, error) {
	switch a.Type {
	case QuestionNoul:
		return json.Marshal(struct {
			Type QuestionType `json:"type"`
			Noul float64      `json:"noul"`
		}{a.Type, a.Noul})
	case QuestionChoice:
		var confidence *float64
		if a.HasConfidence {
			confidence = &a.Confidence
		}
		return json.Marshal(struct {
			Type          QuestionType       `json:"type"`
			Choice        string             `json:"choice"`
			Confidence    *float64           `json:"confidence,omitempty"`
			Probabilities map[string]float64 `json:"probabilities,omitempty"`
		}{a.Type, a.Choice, confidence, a.Probabilities})
	case QuestionScore:
		var confidence *float64
		if a.HasConfidence {
			confidence = &a.Confidence
		}
		return json.Marshal(struct {
			Type          QuestionType               `json:"type"`
			Score         float64                    `json:"score"`
			Confidence    *float64                   `json:"confidence,omitempty"`
			Legend        map[string]json.RawMessage `json:"legend,omitempty"`
			Probabilities map[string]float64         `json:"probabilities,omitempty"`
		}{a.Type, a.Score, confidence, a.Legend, a.Probabilities})
	default:
		return nil, errors.New("answer has an unsupported type")
	}
}

// UnmarshalJSON rejects incomplete answers instead of treating missing values
// as valid zero-probability or zero-score judgments.
func (a *Answer) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type          QuestionType               `json:"type"`
		Noul          *float64                   `json:"noul"`
		Choice        *string                    `json:"choice"`
		Score         *float64                   `json:"score"`
		Confidence    *float64                   `json:"confidence"`
		Probabilities map[string]float64         `json:"probabilities"`
		Legend        map[string]json.RawMessage `json:"legend"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	result := Answer{Type: raw.Type}
	switch raw.Type {
	case QuestionNoul:
		if raw.Noul == nil || *raw.Noul < 0 || *raw.Noul > 1 {
			return errors.New("noul answer requires a probability between 0 and 1")
		}
		result.Noul = *raw.Noul
	case QuestionChoice:
		if raw.Choice == nil || *raw.Choice == "" || raw.Confidence == nil ||
			len(raw.Probabilities) == 0 {
			return errors.New("choice answer requires a choice, confidence, and probabilities")
		}
		if _, ok := raw.Probabilities[*raw.Choice]; !ok {
			return errors.New("choice answer's selected option is missing from probabilities")
		}
		result.Choice = *raw.Choice
		result.Confidence = *raw.Confidence
		result.HasConfidence = true
		result.Probabilities = raw.Probabilities
	case QuestionScore:
		if raw.Score == nil || raw.Confidence == nil || len(raw.Legend) == 0 ||
			len(raw.Probabilities) == 0 {
			return errors.New(
				"score answer requires a score, confidence, legend, and probabilities",
			)
		}
		result.Score = *raw.Score
		result.Confidence = *raw.Confidence
		result.HasConfidence = true
		result.Legend = raw.Legend
		result.Probabilities = raw.Probabilities
	default:
		return errors.New("answer has an unsupported type")
	}
	if result.Confidence < 0 || result.Confidence > 1 {
		return errors.New("answer confidence must be between 0 and 1")
	}
	for _, probability := range result.Probabilities {
		if probability < 0 || probability > 1 {
			return errors.New("answer probabilities must be between 0 and 1")
		}
	}
	*a = result
	return nil
}

// Usage reports the API's token accounting.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Rounding reports the precision applied by an evaluation provider.
type Rounding struct {
	ProbabilityDecimals *int `json:"probabilityDecimals,omitempty"`
	ScoreDecimals       *int `json:"scoreDecimals,omitempty"`
}

// Warning describes a non-fatal provider compatibility or support issue.
type Warning struct {
	Type    string `json:"type"`
	Feature string `json:"feature,omitempty"`
	Details string `json:"details,omitempty"`
	Setting string `json:"setting,omitempty"`
	Message string `json:"message,omitempty"`
}

// Response contains the answers under the same keys as the request's questions.
type Response struct {
	Model            string                     `json:"model"`
	Answers          map[string]Answer          `json:"answers"`
	Usage            Usage                      `json:"usage"`
	RequestID        string                     `json:"-"` // from the HTTP response header, if provided
	Rounding         *Rounding                  `json:"rounding,omitempty"`
	Warnings         []Warning                  `json:"warnings,omitempty"`
	ProviderMetadata map[string]json.RawMessage `json:"provider_metadata,omitempty"`
}

// Model describes an available model or alias.
type Model struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}
