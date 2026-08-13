package httpx

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

// ReadJSON reads and validates one bounded top-level JSON value, then closes
// the body. Protocol decoding belongs to the calling Driver.
func ReadJSON(response *http.Response, limit int64, provider, operation string) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "missing_body", Message: "successful response has no body"}
	}
	defer response.Body.Close()
	reader, closeReader, err := decodedResponseReader(response)
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "unsupported_content_encoding", Message: "response has an unsupported content encoding", RequestID: RequestID(response.Header)}
	}
	if closeReader != nil {
		defer closeReader()
	}
	data, exceeded, err := readBounded(reader, limit)
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "body_read_error", Message: "failed to read response body", RequestID: RequestID(response.Header), Cause: err}
	}
	if exceeded {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "response_too_large", Message: fmt.Sprintf("response exceeds %d bytes", limit), RequestID: RequestID(response.Header)}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "invalid_json", Message: "response is not one valid JSON value", RequestID: RequestID(response.Header)}
	}
	if err := shared.ValidateUnaryJSON(data); err != nil {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "invalid_json", Message: "response is not strict JSON", RequestID: RequestID(response.Header), Cause: err}
	}
	return data, nil
}

func decodedResponseReader(response *http.Response) (io.Reader, func() error, error) {
	values := response.Header.Values("Content-Encoding")
	if len(values) == 0 || len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "identity") {
		return response.Body, nil, nil
	}
	if len(values) != 1 || strings.ContainsRune(values[0], ',') || !strings.EqualFold(strings.TrimSpace(values[0]), "gzip") {
		return nil, nil, fmt.Errorf("unsupported content encoding")
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		return nil, nil, err
	}
	return reader, reader.Close, nil
}

// PrepareResponseBody validates content encoding, exposes decoded bytes, and
// applies a cumulative decoded-byte ceiling to the successful or error body.
func PrepareResponseBody(response *http.Response, limit int64, provider, operation string) error {
	if response == nil || response.Body == nil {
		return &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "missing_body", Message: "response has no body"}
	}
	reader, closeReader, err := decodedResponseReader(response)
	if err != nil {
		return &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "unsupported_content_encoding", Message: "response has an unsupported content encoding", RequestID: RequestID(response.Header)}
	}
	if closeReader != nil {
		response.Body = &decodedReadCloser{reader: reader, body: response.Body, closeDecoded: closeReader}
	}
	response.Header.Del("Content-Encoding")
	overflow := &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation,
		Code: "response_too_large", Message: fmt.Sprintf("response exceeds %d bytes", limit), RequestID: RequestID(response.Header)}
	response.Body = LimitReadCloser(response.Body, limit, overflow)
	return nil
}

type decodedReadCloser struct {
	reader       io.Reader
	body         io.Closer
	closeDecoded func() error
	closed       bool
}

func (r *decodedReadCloser) Read(buffer []byte) (int, error) { return r.reader.Read(buffer) }

func (r *decodedReadCloser) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.closeDecoded()
	if closeErr := r.body.Close(); err == nil {
		err = closeErr
	}
	return err
}

func readBounded(reader io.Reader, limit int64) ([]byte, bool, error) {
	if limit <= 0 {
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

// LimitReadCloser enforces a byte limit while preserving streaming reads. It
// returns overflow when more than limit bytes are observed and permits a body
// whose size is exactly the configured limit.
func LimitReadCloser(body io.ReadCloser, limit int64, overflow error) io.ReadCloser {
	if body == nil || limit <= 0 {
		return body
	}
	return &limitedReadCloser{body: body, remaining: limit, overflow: overflow}
}

type limitedReadCloser struct {
	body      io.ReadCloser
	remaining int64
	overflow  error
	mu        sync.Mutex
	err       error
	closed    bool
}

func (r *limitedReadCloser) overflowError() error {
	if r.overflow != nil {
		return r.overflow
	}
	return fmt.Errorf("response exceeds configured size limit")
}

func (r *limitedReadCloser) Read(buffer []byte) (int, error) {
	r.mu.Lock()
	if len(buffer) == 0 {
		r.mu.Unlock()
		return 0, nil
	}
	if r.err != nil {
		err := r.err
		r.mu.Unlock()
		return 0, err
	}
	if r.closed {
		r.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if r.remaining <= 0 {
		r.mu.Unlock()
		return 0, r.readPastLimit()
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	r.mu.Unlock()
	count, err := r.body.Read(buffer)
	r.mu.Lock()
	r.remaining -= int64(count)
	if err != nil {
		r.err = err
	}
	atLimit := err == nil && r.remaining == 0
	r.mu.Unlock()
	if atLimit {
		if endErr := r.readPastLimit(); endErr != nil {
			return count, endErr
		}
	}
	return count, err
}

func (r *limitedReadCloser) readPastLimit() error {
	// Peek one byte to distinguish an exact-limit body from overflow without
	// retaining the response in memory. A temporary (0, nil) read is retried by
	// the caller instead of being misclassified as overflow.
	var one [1]byte
	count, err := r.body.Read(one[:])
	r.mu.Lock()
	defer r.mu.Unlock()
	if count > 0 {
		r.err = r.overflowError()
		return r.err
	}
	if err != nil {
		r.err = err
	}
	return err
}

func (r *limitedReadCloser) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	return r.body.Close()
}
