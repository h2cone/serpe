package openai

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/protocol/openai/chatcompletions"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/sdkhttp"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

type chatAdapter struct {
	service openai.ChatService
}

func newChatAdapter(options []option.RequestOption) protocolAdapter {
	return &chatAdapter{service: openai.NewChatService(options...)}
}

func (*chatAdapter) capabilities() models.CapabilitySet {
	return chatcompletions.ProtocolCapabilities()
}

func (*chatAdapter) encode(modelID string, request *models.Request, stream bool, config shared.Config) ([]byte, error) {
	if err := chatcompletions.RejectProviderState(request, config.Limits.MaxProviderStateBytes, config.Policy.LenientMapping); err != nil {
		return nil, err
	}
	return chatcompletions.EncodeRequest(modelID, request, stream)
}

func (a *chatAdapter) complete(ctx context.Context, payload []byte, options []option.RequestOption) ([]byte, error) {
	var params openai.ChatCompletionNewParams
	param.SetJSON(payload, &params)
	return sdkhttp.RawJSON(a.service.Completions.New(ctx, params, options...))
}

func (a *chatAdapter) startStream(ctx context.Context, payload []byte, options []option.RequestOption) (func() error, error) {
	var params openai.ChatCompletionNewParams
	param.SetJSON(payload, &params)
	return sdkhttp.StartStream(a.service.Completions.NewStreaming(ctx, params, options...))
}

func (*chatAdapter) decode(raw []byte, requestID string, config shared.Config) (*models.Response, error) {
	return chatcompletions.DecodeResponseJSONWithLimits(raw, requestID, shared.EffectiveToolCallLimits(config.Limits, models.StreamLimits{}))
}

func (*chatAdapter) newSource(reader *sse.Reader, requestID, modelID string, config shared.Config, limits models.StreamLimits) models.EventSource {
	return chatcompletions.NewSSEStreamSource(reader, requestID, modelID, shared.EffectiveToolCallLimits(config.Limits, limits))
}
