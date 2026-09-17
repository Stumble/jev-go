package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

type vercelRequest struct {
	State           any                       `json:"state"`
	Questions       map[string]vercelQuestion `json:"questions"`
	ProviderOptions map[string]any            `json:"providerOptions,omitempty"`
}

type vercelQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions,omitempty"`
	Criteria     any    `json:"criteria,omitempty"`
}

func marshalVercelRequest(request Request) ([]byte, error) {
	questions := make(map[string]vercelQuestion, len(request.Questions))
	for name, question := range request.Questions {
		kind := string(question.Type)
		if question.Type == QuestionNoul {
			kind = "boolean"
		}
		questions[name] = vercelQuestion{
			Type:         kind,
			Instructions: question.Instructions,
			Criteria:     question.Criteria,
		}
	}
	body := vercelRequest{State: request.State, Questions: questions}
	if request.Gateway != nil {
		body.ProviderOptions = map[string]any{"gateway": request.Gateway}
	}
	return json.Marshal(body)
}

type vercelResponse struct {
	Answers          map[string]json.RawMessage `json:"answers"`
	Rounding         *Rounding                  `json:"rounding"`
	Usage            *vercelUsage               `json:"usage"`
	Warnings         []Warning                  `json:"warnings"`
	ProviderMetadata map[string]json.RawMessage `json:"providerMetadata"`
}

type vercelUsage struct {
	InputTokens  *int `json:"inputTokens"`
	OutputTokens *int `json:"outputTokens"`
}

func decodeVercelResponse(data []byte, request Request) (Response, error) {
	var wire vercelResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return Response{}, err
	}
	if wire.Answers == nil {
		return Response{}, errors.New("gateway response is missing answers")
	}
	result := Response{
		Model:            request.Model,
		Answers:          make(map[string]Answer, len(wire.Answers)),
		Rounding:         wire.Rounding,
		Warnings:         wire.Warnings,
		ProviderMetadata: wire.ProviderMetadata,
	}
	if wire.Usage != nil {
		if wire.Usage.InputTokens != nil {
			result.Usage.InputTokens = *wire.Usage.InputTokens
		}
		if wire.Usage.OutputTokens != nil {
			result.Usage.OutputTokens = *wire.Usage.OutputTokens
		}
	}
	for _, name := range sortedQuestionNames(request.Questions) {
		raw, ok := wire.Answers[name]
		if !ok {
			return Response{}, fmt.Errorf("gateway response is missing answer %q", name)
		}
		question := request.Questions[name]
		answer, err := decodeVercelAnswer(raw, question)
		if err != nil {
			return Response{}, fmt.Errorf("gateway answer %q: %w", name, err)
		}
		result.Answers[name] = answer
	}
	if len(wire.Answers) != len(request.Questions) {
		return Response{}, errors.New("gateway response contains unexpected answers")
	}
	applyVercelConfidence(result.Answers, wire.ProviderMetadata)
	return result, nil
}

func decodeVercelAnswer(data []byte, question Question) (Answer, error) {
	var wire struct {
		Type          string             `json:"type"`
		Probability   *float64           `json:"probability"`
		Choice        *string            `json:"choice"`
		Score         *float64           `json:"score"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return Answer{}, err
	}
	switch question.Type {
	case QuestionNoul:
		if wire.Type != "boolean" || wire.Probability == nil || !probability(*wire.Probability) {
			return Answer{}, errors.New("boolean answer requires a probability between 0 and 1")
		}
		return Answer{Type: QuestionNoul, Noul: *wire.Probability}, nil
	case QuestionChoice:
		if wire.Type != "choice" || wire.Choice == nil || *wire.Choice == "" {
			return Answer{}, errors.New("choice answer requires a selected option")
		}
		if len(wire.Probabilities) > 0 {
			if _, ok := wire.Probabilities[*wire.Choice]; !ok {
				return Answer{}, errors.New("selected option is missing from probabilities")
			}
			if err := validateProbabilities(wire.Probabilities); err != nil {
				return Answer{}, err
			}
		}
		return Answer{
			Type:          QuestionChoice,
			Choice:        *wire.Choice,
			Probabilities: wire.Probabilities,
		}, nil
	case QuestionScore:
		if wire.Type != "score" || wire.Score == nil {
			return Answer{}, errors.New("score answer requires a score")
		}
		levels := reflect.ValueOf(question.Criteria).Len()
		if *wire.Score < 0 || *wire.Score > float64(levels-1) {
			return Answer{}, errors.New("score is outside the question's levels")
		}
		if err := validateProbabilities(wire.Probabilities); err != nil {
			return Answer{}, err
		}
		legend, err := scoreLegend(question.Criteria)
		if err != nil {
			return Answer{}, err
		}
		return Answer{
			Type:          QuestionScore,
			Score:         *wire.Score,
			Probabilities: wire.Probabilities,
			Legend:        legend,
		}, nil
	default:
		return Answer{}, errors.New("question has an unsupported type")
	}
}

func scoreLegend(criteria any) (map[string]json.RawMessage, error) {
	levels := reflect.ValueOf(criteria)
	legend := make(map[string]json.RawMessage, levels.Len())
	for i := 0; i < levels.Len(); i++ {
		value, err := json.Marshal(levels.Index(i).Interface())
		if err != nil {
			return nil, fmt.Errorf("encode score level %d: %w", i, err)
		}
		legend[fmt.Sprint(i)] = value
	}
	return legend, nil
}

func applyVercelConfidence(
	answers map[string]Answer,
	metadata map[string]json.RawMessage,
) {
	var typesafe struct {
		Confidence map[string]float64 `json:"confidence"`
	}
	if json.Unmarshal(metadata["typesafe"], &typesafe) != nil {
		return
	}
	for name, confidence := range typesafe.Confidence {
		answer, ok := answers[name]
		if !ok || !probability(confidence) ||
			(answer.Type != QuestionChoice && answer.Type != QuestionScore) {
			continue
		}
		answer.Confidence = confidence
		answer.HasConfidence = true
		answers[name] = answer
	}
}

func validateProbabilities(probabilities map[string]float64) error {
	for _, value := range probabilities {
		if !probability(value) {
			return errors.New("answer probabilities must be between 0 and 1")
		}
	}
	return nil
}

func probability(value float64) bool {
	return value >= 0 && value <= 1
}
