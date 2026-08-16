package builtin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	readCursorVersion = byte(1)
	maxCursorBytes    = 1024
	readCursorDomain  = "serpe.tools.read.cursor.v1"
)

var (
	errCursorInvalid = errors.New("invalid read cursor")
	errCursorStale   = errors.New("stale read cursor")
)

type readCursorPayload struct {
	path     [32]byte
	identity [32]byte
	content  [32]byte
	offset   uint64
	line     uint32
	lines    uint32
}

func cursorDigest(value string) [32]byte { return sha256.Sum256([]byte(value)) }

func (c *cursorCodec) seal(payload readCursorPayload) (string, error) {
	c.counterMu.Lock()
	if c.exhausted {
		c.counterMu.Unlock()
		return "", fmt.Errorf("read cursor nonce space exhausted")
	}
	sequence := c.counter
	if c.counter == math.MaxUint64 {
		c.exhausted = true
	} else {
		c.counter++
	}
	c.counterMu.Unlock()
	var nonce [12]byte
	copy(nonce[:4], c.noncePrefix[:])
	binary.BigEndian.PutUint64(nonce[4:], sequence)
	plain := marshalCursorPayload(payload)
	aad := cursorAAD(c.keyID)
	ciphertext := c.aead.Seal(nil, nonce[:], plain, aad)
	wire := make([]byte, 0, 1+len(c.keyID)+len(nonce)+len(ciphertext))
	wire = append(wire, readCursorVersion)
	wire = append(wire, c.keyID[:]...)
	wire = append(wire, nonce[:]...)
	wire = append(wire, ciphertext...)
	encoded := base64.RawURLEncoding.EncodeToString(wire)
	if len(encoded) > maxCursorBytes {
		return "", fmt.Errorf("read cursor exceeds wire ceiling")
	}
	return encoded, nil
}

func (c *cursorCodec) open(token string) (readCursorPayload, error) {
	if token == "" || len(token) > maxCursorBytes {
		return readCursorPayload{}, errCursorInvalid
	}
	for i := 0; i < len(token); i++ {
		char := token[i]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return readCursorPayload{}, errCursorInvalid
	}
	wire, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(wire) < 1+16+12+c.aead.Overhead() {
		return readCursorPayload{}, errCursorInvalid
	}
	if wire[0] != readCursorVersion {
		return readCursorPayload{}, errCursorInvalid
	}
	if subtle.ConstantTimeCompare(wire[1:17], c.keyID[:]) != 1 {
		return readCursorPayload{}, errCursorStale
	}
	nonce := wire[17:29]
	plain, err := c.aead.Open(nil, nonce, wire[29:], cursorAAD(c.keyID))
	if err != nil {
		return readCursorPayload{}, errCursorInvalid
	}
	payload, ok := unmarshalCursorPayload(plain)
	if !ok {
		return readCursorPayload{}, errCursorInvalid
	}
	return payload, nil
}

func cursorAAD(keyID [16]byte) []byte {
	aad := make([]byte, 0, len(readCursorDomain)+1+len(keyID))
	aad = append(aad, readCursorDomain...)
	aad = append(aad, readCursorVersion)
	return append(aad, keyID[:]...)
}

func marshalCursorPayload(payload readCursorPayload) []byte {
	wire := make([]byte, 0, 3*32+8+4+4)
	wire = append(wire, payload.path[:]...)
	wire = append(wire, payload.identity[:]...)
	wire = append(wire, payload.content[:]...)
	var integers [16]byte
	binary.BigEndian.PutUint64(integers[:8], payload.offset)
	binary.BigEndian.PutUint32(integers[8:12], payload.line)
	binary.BigEndian.PutUint32(integers[12:16], payload.lines)
	return append(wire, integers[:]...)
}

func unmarshalCursorPayload(wire []byte) (readCursorPayload, bool) {
	if len(wire) != 3*32+8+4+4 {
		return readCursorPayload{}, false
	}
	var payload readCursorPayload
	copy(payload.path[:], wire[:32])
	copy(payload.identity[:], wire[32:64])
	copy(payload.content[:], wire[64:96])
	payload.offset = binary.BigEndian.Uint64(wire[96:104])
	payload.line = binary.BigEndian.Uint32(wire[104:108])
	payload.lines = binary.BigEndian.Uint32(wire[108:112])
	if payload.offset > math.MaxInt64 || payload.line == 0 || payload.lines == 0 || payload.lines > 10_000 {
		return readCursorPayload{}, false
	}
	return payload, true
}
