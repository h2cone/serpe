package runtime

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/jsonvalue"
)

func canonicalJSONObject(raw json.RawMessage) (string, error) {
	normalized, err := jsonvalue.CanonicalObject(raw)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

type fingerprintEncoder struct {
	h       hash.Hash
	scratch [8]byte
}

func newFingerprintEncoder() *fingerprintEncoder {
	return &fingerprintEncoder{h: sha256.New()}
}

func (e *fingerprintEncoder) writeUint64(value uint64) {
	binary.BigEndian.PutUint64(e.scratch[:], value)
	_, _ = e.h.Write(e.scratch[:])
}

func (e *fingerprintEncoder) writeBytes(value []byte) {
	e.writeUint64(uint64(len(value)))
	_, _ = e.h.Write(value)
}

func (e *fingerprintEncoder) writeString(value string) {
	e.writeBytes([]byte(value))
}

func (e *fingerprintEncoder) writeBool(value bool) {
	if value {
		e.writeUint64(1)
		return
	}
	e.writeUint64(0)
}

// writeContent hashes validated content blocks via models.Content.CanonicalBytes.
// The agent does not enumerate ContentKind here: which children are legal for a
// tool result is owned by models.Content.Validate, and this encoding follows it.
func (e *fingerprintEncoder) writeContent(content []models.Content) error {
	e.writeUint64(uint64(len(content)))
	for i := range content {
		e.writeUint64(uint64(i))
		canonical, err := content[i].CanonicalBytes()
		if err != nil {
			return fmt.Errorf("content %d: %w", i, err)
		}
		e.writeBytes(canonical)
	}
	return nil
}

func (e *fingerprintEncoder) sum() string {
	return hex.EncodeToString(e.h.Sum(nil))
}

func contentFingerprint(content []models.Content) (string, error) {
	encoder := newFingerprintEncoder()
	encoder.writeString("serpe.runtime.content-fingerprint.v1")
	if err := encoder.writeContent(content); err != nil {
		return "", err
	}
	return encoder.sum(), nil
}

func stepFingerprint(calls []models.ToolCall, results []ToolOutput) (string, error) {
	if len(calls) != len(results) {
		return "", fmt.Errorf("fingerprint: %d calls but %d results", len(calls), len(results))
	}
	encoder := newFingerprintEncoder()
	encoder.writeString("serpe.runtime.step-fingerprint.v1")
	encoder.writeUint64(uint64(len(calls)))
	for i := range calls {
		arguments, err := canonicalJSONObject(calls[i].Arguments)
		if err != nil {
			return "", fmt.Errorf("fingerprint call %d arguments: %w", i, err)
		}
		encoder.writeUint64(uint64(i))
		encoder.writeString(calls[i].Name)
		encoder.writeString(arguments)
		encoder.writeBool(results[i].IsError)
		resultFingerprint, err := contentFingerprint(results[i].Content)
		if err != nil {
			return "", fmt.Errorf("fingerprint call %d result: %w", i, err)
		}
		encoder.writeString(resultFingerprint)
	}
	return encoder.sum(), nil
}
