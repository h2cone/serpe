package responses

import (
	"encoding/json"

	"github.com/h2cone/ouro/core/models"
)

// ProtocolCapabilities returns the adapter capability ceiling.
func ProtocolCapabilities() models.CapabilitySet { return capabilities }

// DecodeResponseJSON decodes a full Responses JSON body into a canonical response.
func DecodeResponseJSON(data []byte, requestID string, stateLimit int64) (*models.Response, error) {
	var wire responseWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, protocolError("response is not valid Responses JSON", err)
	}
	return decodeResponse(wire, requestID, stateLimit)
}
