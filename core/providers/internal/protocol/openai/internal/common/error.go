// Package common contains behavior shared only by OpenAI protocol adapters.
package common

import (
	"strings"

	"github.com/h2cone/serpe/core/models"
)

// NormalizeError classifies an OpenAI wire error shared by Chat Completions
// and Responses.
func NormalizeError(code, typeName, message, operation, requestID string) *models.Error {
	if code == "" {
		code = typeName
	}
	lower := strings.ToLower(code)
	kind := models.ErrorUnknown
	retryable := false
	switch {
	case strings.Contains(lower, "rate"):
		kind, retryable = models.ErrorRateLimited, true
	case strings.Contains(lower, "auth") || strings.Contains(lower, "api_key"):
		kind = models.ErrorAuthentication
	case strings.Contains(lower, "permission") || strings.Contains(lower, "forbidden"):
		kind = models.ErrorPermission
	case strings.Contains(lower, "not_found"):
		kind = models.ErrorNotFound
	case strings.Contains(lower, "invalid") || strings.Contains(lower, "request"):
		kind = models.ErrorInvalidRequest
	case strings.Contains(lower, "timeout"):
		kind, retryable = models.ErrorTimeout, true
	case strings.Contains(lower, "server") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "overload"):
		kind, retryable = models.ErrorUnavailable, true
	}
	return &models.Error{Kind: kind, Provider: "openai", Operation: operation, Code: code, Message: message, RequestID: requestID, Retryable: retryable}
}
