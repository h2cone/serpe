// Package openai contains the codecs for the two OpenAI wire protocols:
// the Responses API and Chat Completions.
package openai

import (
	"encoding/json"
	"net/http"
	"strings"
)

// errorType maps an HTTP status to an OpenAI error "type".
func errorType(status int) string {
	switch {
	case status == 401 || status == 403:
		return "invalid_request_error"
	case status >= 400 && status < 500:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

// encodeError renders an OpenAI-shaped error body. Both OpenAI protocols share
// this shape: {"error":{"message":..., "type":..., "param":null, "code":...}}.
func encodeError(status int, err error) ([]byte, int) {
	if status == 0 {
		status = http.StatusInternalServerError
	}
	msg := "internal error"
	if err != nil {
		msg = err.Error()
	}
	body := map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    errorType(status),
			"param":   nil,
			"code":    nil,
		},
	}
	data, _ := json.Marshal(body)
	return data, status
}

// dataURL builds a data: URL from an inline image.
func dataURL(mediaType, data string) string {
	if mediaType == "" {
		mediaType = "image/png"
	}
	return "data:" + mediaType + ";base64," + data
}

// parseDataURL extracts media type and base64 data from a data: URL. ok is false
// when url is not a data URL.
func parseDataURL(url string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(url, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(url, "data:")
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	mt := rest[:comma]
	data = rest[comma+1:]
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	return mt, data, true
}
