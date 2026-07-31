package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/h2cone/ouro/core/models"
)

// ReadJSON reads one bounded top-level JSON value, closes the body, and decodes
// it into target.
func ReadJSON(response *http.Response, limit int64, provider, operation string, target any) error {
	if response == nil || response.Body == nil {
		return &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "missing_body", Message: "successful response has no body"}
	}
	defer response.Body.Close()
	data, exceeded, err := readBounded(response.Body, limit)
	if err != nil {
		return &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "body_read_error", Message: "failed to read response body", RequestID: RequestID(response.Header), Cause: err}
	}
	if exceeded {
		return &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "response_too_large", Message: fmt.Sprintf("response exceeds %d bytes", limit), RequestID: RequestID(response.Header)}
	}
	if len(bytes.TrimSpace(data)) == 0 || !json.Valid(data) {
		return &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "invalid_json", Message: "response is not one valid JSON value", RequestID: RequestID(response.Header)}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return &models.Error{Kind: models.ErrorProtocol, Provider: provider, Operation: operation, Code: "invalid_response", Message: "response JSON has an invalid protocol shape", RequestID: RequestID(response.Header), Cause: err}
	}
	return nil
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
