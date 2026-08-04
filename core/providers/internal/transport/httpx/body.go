package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/h2cone/ouro/core/models"
)

// ReadJSON reads and validates one bounded top-level JSON value, then closes
// the body. Protocol decoding belongs to the calling Driver.
func ReadJSON(response *http.Response, limit int64, provider, operation string) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "missing_body", Message: "successful response has no body"}
	}
	defer response.Body.Close()
	data, exceeded, err := readBounded(response.Body, limit)
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "body_read_error", Message: "failed to read response body", RequestID: RequestID(response.Header), Cause: err}
	}
	if exceeded {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "response_too_large", Message: fmt.Sprintf("response exceeds %d bytes", limit), RequestID: RequestID(response.Header)}
	}
	if len(bytes.TrimSpace(data)) == 0 || !json.Valid(data) {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "invalid_json", Message: "response is not one valid JSON value", RequestID: RequestID(response.Header)}
	}
	return data, nil
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
	defer r.mu.Unlock()
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.remaining <= 0 {
		return 0, r.readPastLimit()
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.body.Read(buffer)
	r.remaining -= int64(count)
	if err == nil && r.remaining == 0 {
		if endErr := r.readPastLimit(); endErr != nil {
			return count, endErr
		}
	}
	if err != nil {
		r.err = err
	}
	return count, err
}

func (r *limitedReadCloser) readPastLimit() error {
	// Peek one byte to distinguish an exact-limit body from overflow without
	// retaining the response in memory. A temporary (0, nil) read is retried by
	// the caller instead of being misclassified as overflow.
	var one [1]byte
	count, err := r.body.Read(one[:])
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
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.body.Close()
}
