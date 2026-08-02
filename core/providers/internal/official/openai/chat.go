package openai

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/openai/chatcompletions"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/sse"
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
	completion, err := a.service.Completions.New(ctx, params, options...)
	if err != nil {
		return nil, err
	}
	return []byte(completion.RawJSON()), nil
}

func (a *chatAdapter) startStream(ctx context.Context, payload []byte, options []option.RequestOption) (func() error, error) {
	var params openai.ChatCompletionNewParams
	param.SetJSON(payload, &params)
	stream := a.service.Completions.NewStreaming(ctx, params, options...)
	if err := stream.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream.Close, nil
}

func (*chatAdapter) decode(raw []byte, requestID string, _ shared.Config) (*models.Response, error) {
	return chatcompletions.DecodeResponseJSON(raw, requestID)
}

func (*chatAdapter) newSource(reader *sse.Reader, requestID, modelID string, _ shared.Config) models.EventSource {
	return chatcompletions.NewSSEStreamSource(reader, requestID, modelID)
}
