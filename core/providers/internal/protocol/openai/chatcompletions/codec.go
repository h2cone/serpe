package chatcompletions

import (
	"encoding/json"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

// ProtocolCapabilities returns the adapter capability ceiling.
func ProtocolCapabilities() models.CapabilitySet { return capabilities }

// DecodeResponseJSON decodes a full Chat Completions JSON body.
func DecodeResponseJSON(data []byte, requestID string) (*models.Response, error) {
	var wire chatResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, protocolError("response is not valid Chat Completions JSON", err)
	}
	return decodeResponse(wire, requestID)
}

// RejectProviderState enforces that Chat Completions has no resumable state.
func RejectProviderState(req *models.Request, stateLimit int64, lenient bool) error {
	for i := range req.Messages {
		accepted, err := shared.ValidateProviderState(req.Messages[i].ProviderState, protocol, stateLimit, lenient)
		if err != nil {
			return err
		}
		if accepted {
			return &models.Error{Kind: models.ErrorUnsupportedFeature, Provider: "openai", Operation: "encode", Code: "provider_state", Message: "Chat Completions does not define resumable opaque provider state"}
		}
	}
	return nil
}
