package sdkhttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/transport/httpx"
)

// ErrorInfo is the vendor-specific portion of an official SDK error.
type ErrorInfo struct {
	Kind          models.ErrorKind
	Status        int
	Code          string
	Message       string
	RequestID     string
	Header        http.Header
	Retryable     bool
	OmitRequestID bool
	CaptureHeader bool
}

// NormalizeError applies the error contract shared by all official SDKs.
func NormalizeError(err error, provider, operation string, capture *Capture, secrets []string, fallback string, parse func(error) (ErrorInfo, bool)) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return models.ContextError(provider, operation, err)
	}
	var modelErr *models.Error
	if errors.As(err, &modelErr) {
		return modelErr
	}
	if info, ok := parse(err); ok {
		kind, retryable := info.Kind, info.Retryable
		if kind == "" {
			kind, retryable = httpx.StatusKind(info.Status)
		}
		header := info.Header
		if header == nil && capture != nil && info.CaptureHeader {
			header = capture.Header
		}
		requestID := info.RequestID
		if requestID == "" && !info.OmitRequestID {
			if capture != nil {
				requestID = capture.RequestID()
			}
			if requestID == "" {
				requestID = httpx.RequestID(header)
			}
		}
		return &models.Error{
			Kind: kind, Provider: provider, Operation: operation,
			HTTPStatus: info.Status, Code: httpx.Redact(info.Code, secrets), Message: httpx.Redact(info.Message, secrets),
			RequestID: requestID, RetryAfter: httpx.RetryAfter(header.Get("Retry-After")), Retryable: retryable,
		}
	}
	return &models.Error{Kind: models.ErrorUnavailable, Provider: provider, Operation: operation, Message: fallback, Retryable: true}
}
