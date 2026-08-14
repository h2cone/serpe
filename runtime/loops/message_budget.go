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

const (
	perCallRawReserve = 1 << 10
	rawQuotaDivisor   = 6
)

type toolAdmission struct {
	quota          int64
	reserved       int64
	assistantBytes int64
	minimumGroup   int64
}

func toolExchangeAdmission(calls []models.ToolCall, assistant models.Message, sessionCeiling, retainedRemaining, canonicalRemaining int64) (toolAdmission, error) {
	assistantBytes, err := sessionwire.MessageFragmentSize(assistant)
	if err != nil {
		return toolAdmission{}, fmt.Errorf("%w: assistant message cannot be encoded", ErrInvalidModelResponse)
	}
	_, minimumResultBytes, err := minimumToolResultMessage(calls)
	if err != nil {
		return toolAdmission{}, fmt.Errorf("%w: cannot frame tool results", ErrInvalidModelResponse)
	}
	fixedReserve, ok := safeAddBytes(minimumResultBytes, int64(len(calls))*perCallRawReserve)
	if !ok || fixedReserve > sessionCeiling {
		return toolAdmission{}, fmt.Errorf("%w: tool result message cannot fit session message ceiling", ErrRunLimit)
	}
	minimumGroup, ok := safeAddBytes(assistantBytes, fixedReserve)
	if !ok || minimumGroup > retainedRemaining || minimumGroup > canonicalRemaining {
		return toolAdmission{}, fmt.Errorf("%w: tool exchange cannot fit remaining run budget", ErrRunLimit)
	}
	rawQuota := (sessionCeiling - fixedReserve) / rawQuotaDivisor
	if remaining := retainedRemaining - minimumGroup; rawQuota > remaining {
		rawQuota = remaining
	}
	if remaining := canonicalRemaining - minimumGroup; rawQuota > remaining {
		rawQuota = remaining
	}
	return toolAdmission{
		quota: rawQuota, reserved: fixedReserve,
		assistantBytes: assistantBytes, minimumGroup: minimumGroup,
	}, nil
}

func (a *toolAdmission) clampTo(ceiling int64) {
	if ceiling > 0 && a.quota > ceiling {
		a.quota = ceiling
	}
}

func minimumRawQuota(calls int) int64 { return int64(calls) * perCallRawReserve }

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
	body := strings.Repeat("x", perCallRawReserve)
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
