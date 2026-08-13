package loops

import (
	"fmt"
	"math"
	"strings"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/sessionwire"
)

func canonicalToolHistoryBytes(messages []models.Message) (int64, error) {
	var total int64
	for _, span := range toolExchangeSpans(messages) {
		if span.start < 0 || span.end != span.start+2 || span.end > len(messages) {
			return 0, fmt.Errorf("%w: invalid tool exchange span", ErrInvalidModelResponse)
		}
		size, err := canonicalToolExchangeBytes(messages[span.start], messages[span.start+1])
		if err != nil {
			return 0, err
		}
		if size > math.MaxInt64-total {
			return 0, fmt.Errorf("%w: canonical tool history size overflow", ErrRunLimit)
		}
		total += size
	}
	return total, nil
}

func canonicalToolExchangeBytes(assistant, result models.Message) (int64, error) {
	left, err := sessionwire.MessageFragmentSize(assistant)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid assistant tool message", ErrInvalidModelResponse)
	}
	right, err := sessionwire.MessageFragmentSize(result)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid tool result message", ErrInvalidModelResponse)
	}
	// The canonical exchange frame additionally retains provider continuation
	// state, which the public session detail projection deliberately omits.
	providerBytes := int64(0)
	if assistant.ProviderState != nil {
		providerBytes = int64(len(assistant.ProviderState.Provider)) + int64(len(assistant.ProviderState.Data)) + 32
	}
	if left > math.MaxInt64-right || left+right > math.MaxInt64-providerBytes-32 {
		return 0, fmt.Errorf("%w: canonical tool exchange size overflow", ErrRunLimit)
	}
	return left + right + providerBytes + 32, nil
}

func minimumToolResultMessage(calls []models.ToolCall) (models.Message, int64, error) {
	contents := make([]models.Content, len(calls))
	for i := range calls {
		contents[i] = models.ToolResultContent(calls[i].ID, calls[i].Name, true, models.Text(""))
	}
	message := models.NewUserMessage(contents...)
	size, err := sessionwire.MessageFragmentSize(message)
	if err != nil {
		return models.Message{}, 0, err
	}
	return message, size, nil
}

func providerSkeletonToolResultMessage(calls []models.ToolCall) models.Message {
	contents := make([]models.Content, len(calls))
	body := strings.Repeat("x", 1<<10)
	for i := range calls {
		contents[i] = models.ToolResultContent(calls[i].ID, calls[i].Name, true, models.Text(body))
	}
	return models.NewUserMessage(contents...)
}

func safeAddBytes(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, false
	}
	return left + right, true
}
