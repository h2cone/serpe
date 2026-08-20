package shared

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/h2cone/serpe/core/models"
)

// ValidateModelID checks generic upstream model ID invariants.
func ValidateModelID(upstreamModelID, provider string) error {
	if upstreamModelID == "" || strings.TrimSpace(upstreamModelID) != upstreamModelID {
		return &models.Error{Kind: models.ErrorInvalidRequest, Provider: provider, Operation: "bind_model", Code: "invalid_model", Message: "model ID is empty or has surrounding whitespace"}
	}
	if strings.IndexFunc(upstreamModelID, unicode.IsControl) >= 0 {
		return &models.Error{Kind: models.ErrorInvalidRequest, Provider: provider, Operation: "bind_model", Code: "invalid_model", Message: "model ID contains control characters"}
	}
	return nil
}

// ValidateProviderState checks protocol ownership and configured size. Lenient
// mapping may discard state owned by another protocol, but never malformed
// same-protocol state.
func ValidateProviderState(state *models.ProviderState, protocol string, limit int64, lenient bool) (bool, error) {
	if state == nil {
		return false, nil
	}
	if state.Provider != protocol {
		if lenient {
			return false, nil
		}
		return false, &models.Error{Kind: models.ErrorUnsupportedFeature, Provider: providerFromProtocol(protocol), Operation: "validate", Code: "foreign_provider_state", Message: fmt.Sprintf("provider state %q cannot be sent through %q", state.Provider, protocol)}
	}
	if int64(len(state.Data)) > limit {
		return false, &models.Error{Kind: models.ErrorInvalidRequest, Provider: providerFromProtocol(protocol), Operation: "validate", Code: "provider_state_too_large", Message: fmt.Sprintf("provider state exceeds %d bytes", limit)}
	}
	if err := state.Validate(); err != nil {
		return false, &models.Error{Kind: models.ErrorInvalidRequest, Provider: providerFromProtocol(protocol), Operation: "validate", Code: "invalid_provider_state", Message: err.Error()}
	}
	return true, nil
}

func providerFromProtocol(protocol string) string {
	if strings.HasPrefix(protocol, "openai.") {
		return "openai"
	}
	if strings.HasPrefix(protocol, "anthropic.") {
		return "anthropic"
	}
	if strings.HasPrefix(protocol, "gemini.") {
		return "google"
	}
	return ""
}

// MergeExtension merges one namespaced object into an encoded request while
// rejecting every canonical key, including omitted canonical fields.
func MergeExtension(encoded []byte, extensions map[string]json.RawMessage, namespace string, reserved ...string) ([]byte, error) {
	raw, exists := extensions[namespace]
	if !exists {
		return encoded, nil
	}
	var base map[string]json.RawMessage
	if err := DecodeJSON(encoded, &base); err != nil {
		return nil, fmt.Errorf("encode canonical request: %w", err)
	}
	var extension map[string]json.RawMessage
	if err := DecodeJSON(raw, &extension); err != nil || extension == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: providerFromProtocol(namespace), Operation: "encode", Code: "invalid_extension", Message: "provider extension must be a JSON object"}
	}
	blocked := make(map[string]struct{}, len(reserved))
	for _, key := range reserved {
		blocked[key] = struct{}{}
	}
	for key, value := range extension {
		if _, exists := blocked[key]; exists {
			return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: providerFromProtocol(namespace), Operation: "encode", Code: "extension_override", Message: fmt.Sprintf("extension cannot override canonical field %q", key)}
		}
		base[key] = append(json.RawMessage(nil), value...)
	}
	return EncodeJSON(base)
}
