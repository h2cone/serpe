// Package google implements Gemini GenerateContent wire semantics.
package google

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

const protocol = "gemini.generate_content"

var capabilities = models.Capabilities(
	models.CapabilityText,
	models.CapabilityImageInput,
	models.CapabilityTools,
	models.CapabilityParallelTools,
	models.CapabilityJSONOutput,
	models.CapabilityJSONSchema,
	models.CapabilityReasoningSummary,
	models.CapabilityProviderState,
	models.CapabilityMultipleCandidates,
)

// ProtocolCapabilities returns the adapter capability ceiling.
func ProtocolCapabilities() models.CapabilitySet { return capabilities }

// DecodeResponseJSON decodes a full Gemini generateContent JSON body.
func DecodeResponseJSON(data []byte, requestID, fallbackModel string, stateLimit int64) (*models.Response, error) {
	var wire responseWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, protocolError("response is not valid Gemini GenerateContent JSON", err)
	}
	return decodeResponse(wire, requestID, fallbackModel, stateLimit)
}

// ValidateModelID enforces Gemini bare model ID rules and returns a path-escaped ID.
func ValidateModelID(modelID string) (escaped string, err error) {
	if err := shared.ValidateModelID(modelID, "google"); err != nil {
		return "", err
	}
	if modelID == "." || modelID == ".." || strings.ContainsAny(modelID, "/\\?#:%") || strings.IndexFunc(modelID, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.')
	}) >= 0 {
		return "", &models.Error{Kind: models.ErrorInvalidRequest, Provider: "google", Operation: "bind_model", Code: "invalid_model", Message: "Gemini model ID must be a safe bare model name"}
	}
	return url.PathEscape(modelID), nil
}
