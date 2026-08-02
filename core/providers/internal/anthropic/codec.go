package anthropic

import (
	"encoding/json"

	"github.com/h2cone/ouro/core/models"
)

// ProtocolCapabilities returns the adapter capability ceiling.
func ProtocolCapabilities() models.CapabilitySet { return capabilities }

// EncodeRequest encodes a canonical request for Anthropic Messages.
func EncodeRequest(modelID string, req *models.Request, stream, lenient bool, stateLimit int64) ([]byte, error) {
	return encodeRequest(modelID, req, stream, lenient, stateLimit)
}

// DecodeResponseJSON decodes a full Anthropic message JSON body.
func DecodeResponseJSON(data []byte, requestID string, stateLimit int64) (*models.Response, error) {
	var wire messageWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, protocolError("response is not valid Anthropic Messages JSON", err)
	}
	return decodeResponse(wire, requestID, stateLimit)
}
