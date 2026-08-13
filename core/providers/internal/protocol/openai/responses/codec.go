// Package responses implements OpenAI Responses wire semantics.
package responses

import (
	"encoding/json"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

const protocol = "openai.responses"

var capabilities = models.Capabilities(
	models.CapabilityText,
	models.CapabilityImageInput,
	models.CapabilityTools,
	models.CapabilityParallelTools,
	models.CapabilityJSONOutput,
	models.CapabilityJSONSchema,
	models.CapabilityReasoningSummary,
	models.CapabilityProviderState,
	models.CapabilityToolResultImage,
)

// ProtocolCapabilities returns the adapter capability ceiling.
func ProtocolCapabilities() models.CapabilitySet { return capabilities }

// DecodeResponseJSON decodes a full Responses JSON body into a canonical response.
func DecodeResponseJSON(data []byte, requestID string, stateLimit int64) (*models.Response, error) {
	return DecodeResponseJSONWithLimits(data, requestID, stateLimit, shared.DefaultToolCallLimits())
}

// DecodeResponseJSONWithLimits decodes with provider/run tool-call ceilings.
func DecodeResponseJSONWithLimits(data []byte, requestID string, stateLimit int64, limits shared.ToolCallLimits) (*models.Response, error) {
	if err := shared.ValidateUnaryJSON(data); err != nil {
		return nil, protocolError("response is not strict JSON", err)
	}
	var wire responseWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, protocolError("response is not valid Responses JSON", err)
	}
	return decodeResponse(wire, requestID, stateLimit, shared.NewToolCallGuard(limits))
}
