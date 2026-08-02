package openai

import (
	"context"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	sdkresponses "github.com/openai/openai-go/v3/responses"

	"github.com/h2cone/ouro/core/models"
	defaultresponses "github.com/h2cone/ouro/core/providers/internal/openai/responses"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/sse"
)

type responsesAdapter struct {
	service sdkresponses.ResponseService
}

func newResponsesAdapter(options []option.RequestOption) protocolAdapter {
	return &responsesAdapter{service: sdkresponses.NewResponseService(options...)}
}

func (*responsesAdapter) capabilities() models.CapabilitySet {
	return defaultresponses.ProtocolCapabilities()
}

func (*responsesAdapter) encode(modelID string, request *models.Request, stream bool, config shared.Config) ([]byte, error) {
	return defaultresponses.EncodeRequest(modelID, request, stream, config.Policy.LenientMapping, config.Limits.MaxProviderStateBytes)
}

func (a *responsesAdapter) complete(ctx context.Context, payload []byte, options []option.RequestOption) ([]byte, error) {
	var params sdkresponses.ResponseNewParams
	param.SetJSON(payload, &params)
	response, err := a.service.New(ctx, params, options...)
	if err != nil {
		return nil, err
	}
	return []byte(response.RawJSON()), nil
}

func (a *responsesAdapter) startStream(ctx context.Context, payload []byte, options []option.RequestOption) (func() error, error) {
	var params sdkresponses.ResponseNewParams
	param.SetJSON(payload, &params)
	stream := a.service.NewStreaming(ctx, params, options...)
	if err := stream.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream.Close, nil
}

func (*responsesAdapter) decode(raw []byte, requestID string, config shared.Config) (*models.Response, error) {
	return defaultresponses.DecodeResponseJSON(raw, requestID, config.Limits.MaxProviderStateBytes)
}

func (*responsesAdapter) newSource(reader *sse.Reader, requestID, modelID string, config shared.Config) models.EventSource {
	return defaultresponses.NewSSEStreamSource(reader, requestID, modelID, config.Policy.IgnoreUnknownEvent, config.Limits.MaxProviderStateBytes)
}
