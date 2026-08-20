// Package anthropic implements Anthropic Messages wire semantics.
package anthropic

import (
	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

const protocol = "anthropic.messages"

var capabilities = models.Capabilities(
	models.CapabilityText,
	models.CapabilityImageInput,
	models.CapabilityTools,
	models.CapabilityParallelTools,
	models.CapabilityJSONSchema,
	models.CapabilityReasoningSummary,
	models.CapabilityProviderState,
	models.CapabilityToolResultImage,
)

// ProtocolCapabilities returns the adapter capability ceiling.
func ProtocolCapabilities() models.CapabilitySet { return capabilities }

// DecodeResponseJSON decodes a full Anthropic message JSON body.
func DecodeResponseJSON(data []byte, requestID string, stateLimit int64) (*models.Response, error) {
	return DecodeResponseJSONWithLimits(data, requestID, stateLimit, shared.DefaultToolCallLimits())
}

// DecodeResponseJSONWithLimits decodes with provider/run tool-call ceilings.
func DecodeResponseJSONWithLimits(data []byte, requestID string, stateLimit int64, limits shared.ToolCallLimits) (*models.Response, error) {
	if err := shared.ValidateUnaryJSON(data); err != nil {
		return nil, protocolError("response is not strict JSON", err)
	}
	var wire messageWire
	if err := shared.DecodeJSON(data, &wire); err != nil {
		return nil, protocolError("response is not valid Anthropic Messages JSON", err)
	}
	return decodeResponse(wire, requestID, stateLimit, shared.NewToolCallGuard(limits))
}
