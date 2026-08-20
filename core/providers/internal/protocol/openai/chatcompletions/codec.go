// Package chatcompletions implements OpenAI Chat Completions wire semantics.
package chatcompletions

import (
	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

const protocol = "openai.chat_completions"

var capabilities = models.Capabilities(
	models.CapabilityText,
	models.CapabilityImageInput,
	models.CapabilityTools,
	models.CapabilityParallelTools,
	models.CapabilityJSONOutput,
	models.CapabilityJSONSchema,
	models.CapabilityMultipleCandidates,
)

// ProtocolCapabilities returns the adapter capability ceiling.
func ProtocolCapabilities() models.CapabilitySet { return capabilities }

// DecodeResponseJSON decodes a full Chat Completions JSON body.
func DecodeResponseJSON(data []byte, requestID string) (*models.Response, error) {
	return DecodeResponseJSONWithLimits(data, requestID, shared.DefaultToolCallLimits())
}

// DecodeResponseJSONWithLimits decodes with provider/run tool-call ceilings.
func DecodeResponseJSONWithLimits(data []byte, requestID string, limits shared.ToolCallLimits) (*models.Response, error) {
	if err := shared.ValidateUnaryJSON(data); err != nil {
		return nil, protocolError("response is not strict JSON", err)
	}
	var wire chatResponse
	if err := shared.DecodeJSON(data, &wire); err != nil {
		return nil, protocolError("response is not valid Chat Completions JSON", err)
	}
	return decodeResponse(wire, requestID, shared.NewToolCallGuard(limits))
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
