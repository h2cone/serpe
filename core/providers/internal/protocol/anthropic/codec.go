// Package anthropic implements Anthropic Messages wire semantics.
package anthropic

import (
	"encoding/json"

	"github.com/h2cone/ouro/core/models"
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
)

// ProtocolCapabilities returns the adapter capability ceiling.
func ProtocolCapabilities() models.CapabilitySet { return capabilities }

// DecodeResponseJSON decodes a full Anthropic message JSON body.
func DecodeResponseJSON(data []byte, requestID string, stateLimit int64) (*models.Response, error) {
	var wire messageWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, protocolError("response is not valid Anthropic Messages JSON", err)
	}
	return decodeResponse(wire, requestID, stateLimit)
}
