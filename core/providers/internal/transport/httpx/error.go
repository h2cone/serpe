package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

// errorEnvelope covers OpenAI-style nested errors and OpenAI-compatible
// providers (for example xAI) that put a string in "error" and a top-level code.
type errorEnvelope struct {
	Error   json.RawMessage `json:"error"`
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
}

type errorObject struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
	Status  string          `json:"status"`
}

// DecodeError consumes a bounded non-success response and normalizes it.
func DecodeError(response *http.Response, provider, operation string, limit int64, redact []string) error {
	if response == nil {
		return &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "missing_response", Message: "HTTP transport returned no response"}
	}
	if response.Body == nil {
		kind, retryable := StatusKind(response.StatusCode)
		return &models.Error{Kind: kind, Provider: provider, Operation: operation, HTTPStatus: response.StatusCode, Message: http.StatusText(response.StatusCode), RequestID: RequestID(response.Header), Retryable: retryable}
	}
	defer response.Body.Close()
	reader, closeReader, decodeErr := decodedResponseReader(response)
	if closeReader != nil {
		defer closeReader()
	}
	var data []byte
	var exceeded bool
	readErr := decodeErr
	if readErr == nil {
		data, exceeded, readErr = readBounded(reader, limit)
	}
	message := http.StatusText(response.StatusCode)
	code := ""
	var strictErr error
	if readErr == nil && !exceeded {
		trimmed := bytes.TrimSpace(data)
		switch {
		case len(trimmed) == 0:
		case errorBodyLooksJSON(response.Header.Get("Content-Type"), trimmed):
			if strictErr = shared.ValidateErrorJSON(data); strictErr != nil {
				message = "provider returned an invalid JSON error response"
			} else if extracted, extractedCode, ok := parseErrorBody(data); ok {
				message = shared.FirstNonempty(extracted, message)
				code = extractedCode
			}
		default:
			message = "provider returned a non-JSON error response"
		}
	} else if exceeded {
		message = fmt.Sprintf("provider error response exceeds %d bytes", limit)
	} else if readErr != nil {
		message = "failed to read provider error response"
	}
	message = Redact(message, redact)
	code = Redact(code, redact)
	kind, retryable := StatusKind(response.StatusCode)
	if strictErr != nil {
		kind, retryable, code = models.ErrorProtocol, false, "invalid_json"
	}
	return &models.Error{
		Kind: kind, Provider: provider, Operation: operation,
		HTTPStatus: response.StatusCode, Code: code, Message: message,
		RequestID: RequestID(response.Header), RetryAfter: RetryAfter(response.Header.Get("Retry-After")), Retryable: retryable,
		Cause: errors.Join(readErr, strictErr),
	}
}

func errorBodyLooksJSON(contentType string, trimmed []byte) bool {
	if HasMediaType(contentType, "application/json") || strings.HasSuffix(strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])), "+json") {
		return true
	}
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// parseErrorBody extracts a public message and code from common provider
// error JSON shapes. ok is false when the body is not JSON or carries no
// recognizable message/code fields.
func parseErrorBody(data []byte) (message, code string, ok bool) {
	var envelope errorEnvelope
	if json.Unmarshal(data, &envelope) != nil {
		return "", "", false
	}
	if len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		var obj errorObject
		if json.Unmarshal(envelope.Error, &obj) == nil {
			message = obj.Message
			code = shared.FirstNonempty(rawCode(obj.Code), obj.Type, obj.Status)
		} else {
			var text string
			if json.Unmarshal(envelope.Error, &text) == nil {
				message = text
			}
		}
	}
	message = shared.FirstNonempty(message, envelope.Message)
	code = shared.FirstNonempty(code, rawCode(envelope.Code), envelope.Type)
	if message == "" && code == "" {
		return "", "", false
	}
	return message, code, true
}

// StatusKind maps an HTTP response status to the canonical model error kind
// and retryability shared by all provider transports.
func StatusKind(status int) (models.ErrorKind, bool) {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return models.ErrorInvalidRequest, false
	case http.StatusUnauthorized:
		return models.ErrorAuthentication, false
	case http.StatusForbidden:
		return models.ErrorPermission, false
	case http.StatusNotFound:
		return models.ErrorNotFound, false
	case http.StatusConflict:
		return models.ErrorConflict, false
	case http.StatusTooManyRequests:
		return models.ErrorRateLimited, true
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return models.ErrorTimeout, true
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return models.ErrorUnavailable, true
	default:
		if status >= 500 {
			return models.ErrorUnavailable, true
		}
		return models.ErrorUnknown, false
	}
}

func rawCode(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) == nil {
		return number.String()
	}
	return ""
}

// RetryAfter parses the standard Retry-After header as seconds or an HTTP
// date. Past and invalid dates return zero.
func RetryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if duration := time.Until(when); duration > 0 {
			return duration
		}
	}
	return 0
}

// Redact replaces configured secrets in a safely public error field.
func Redact(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

// RequestID returns a safely public provider request identifier.
func RequestID(header http.Header) string {
	for _, name := range []string{"x-request-id", "request-id", "x-goog-request-id"} {
		if value := header.Get(name); value != "" {
			return value
		}
		// Custom Doers may construct response headers as map literals instead of
		// canonicalizing them through net/http.
		for key, values := range header {
			if strings.EqualFold(key, name) && len(values) > 0 && values[0] != "" {
				return values[0]
			}
		}
	}
	return ""
}
