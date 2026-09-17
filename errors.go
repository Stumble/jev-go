package jev

import (
	"encoding/json"
	"net/http"
)

func parseAPIError(status int, requestID string, data []byte) *APIError {
	result := &APIError{StatusCode: status, RequestID: requestID, Message: http.StatusText(status), Body: data}
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
		Code    string          `json:"code"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return result
	}
	if envelope.Message != "" {
		result.Message = envelope.Message
	} else if envelope.Detail != "" {
		result.Message = envelope.Detail
	}
	result.Code = envelope.Code
	if len(envelope.Error) > 0 {
		var message string
		if json.Unmarshal(envelope.Error, &message) == nil && message != "" {
			result.Message = message
		} else {
			var nested struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			}
			if json.Unmarshal(envelope.Error, &nested) == nil {
				if nested.Message != "" {
					result.Message = nested.Message
				}
				if nested.Code != "" {
					result.Code = nested.Code
				}
			}
		}
	}
	return result
}
