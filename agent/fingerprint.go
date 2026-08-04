package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/internal/jsonvalue"
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

func (e *fingerprintEncoder) writeContent(content []models.Content) error {
	e.writeUint64(uint64(len(content)))
	for i := range content {
		block := content[i]
		e.writeUint64(uint64(i))
		e.writeString(string(block.Kind))
		switch block.Kind {
		case models.ContentText:
			if block.Text == nil {
				return fmt.Errorf("content %d: text value is missing", i)
			}
			e.writeString(block.Text.Text)
		case models.ContentImage:
			if block.Image == nil {
				return fmt.Errorf("content %d: image value is missing", i)
			}
			e.writeString(block.Image.URI)
			e.writeString(block.Image.MIMEType)
			e.writeString(string(block.Image.Detail))
			e.writeBytes(block.Image.Data)
		default:
			return fmt.Errorf("content %d: unsupported kind %q", i, block.Kind)
		}
	}
	return nil
}

func (e *fingerprintEncoder) sum() string {
	return hex.EncodeToString(e.h.Sum(nil))
}

func contentFingerprint(content []models.Content) (string, error) {
	encoder := newFingerprintEncoder()
	encoder.writeString("ouro.agent.content-fingerprint.v1")
	if err := encoder.writeContent(content); err != nil {
		return "", err
	}
	return encoder.sum(), nil
}

func stepFingerprint(calls []models.ToolCall, results []ToolResult) (string, error) {
	if len(calls) != len(results) {
		return "", fmt.Errorf("fingerprint: %d calls but %d results", len(calls), len(results))
	}
	encoder := newFingerprintEncoder()
	encoder.writeString("ouro.agent.step-fingerprint.v1")
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
