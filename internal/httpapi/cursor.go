package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math"

	"github.com/h2cone/serpe/runtime/sessions"
)

const (
	cursorVersion      byte = 1
	cursorKindList     byte = 1
	cursorKindDetail   byte = 2
	maxCursorWireBytes      = 1024
)

var (
	errCursorInvalid = errors.New("invalid cursor")
	errCursorStale   = errors.New("stale cursor")
)

type cursorCodec struct {
	key   [32]byte
	keyID [16]byte
}

func newCursorCodec(random io.Reader) (*cursorCodec, error) {
	codec := &cursorCodec{}
	if _, err := io.ReadFull(random, codec.key[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(random, codec.keyID[:]); err != nil {
		return nil, err
	}
	return codec, nil
}

func (c *cursorCodec) encodeList(afterID string) (string, error) {
	if !sessions.ValidID(afterID) {
		return "", errCursorInvalid
	}
	payload := make([]byte, 2+len(afterID))
	binary.BigEndian.PutUint16(payload[:2], uint16(len(afterID)))
	copy(payload[2:], afterID)
	return c.encode(cursorKindList, payload)
}

func (c *cursorCodec) decodeList(token string) (string, error) {
	payload, err := c.decode(token, cursorKindList)
	if err != nil {
		return "", err
	}
	if len(payload) < 2 {
		return "", errCursorInvalid
	}
	length := int(binary.BigEndian.Uint16(payload[:2]))
	if length == 0 || length != len(payload)-2 {
		return "", errCursorInvalid
	}
	id := string(payload[2:])
	if !sessions.ValidID(id) {
		return "", errCursorInvalid
	}
	return id, nil
}

func (c *cursorCodec) encodeDetail(sessionID string, before, snapshot int) (string, error) {
	if !sessions.ValidID(sessionID) || before < 0 || snapshot < 0 || before > snapshot {
		return "", errCursorInvalid
	}
	payload := make([]byte, 2+len(sessionID)+16)
	binary.BigEndian.PutUint16(payload[:2], uint16(len(sessionID)))
	copy(payload[2:], sessionID)
	offset := 2 + len(sessionID)
	binary.BigEndian.PutUint64(payload[offset:offset+8], uint64(before))
	binary.BigEndian.PutUint64(payload[offset+8:offset+16], uint64(snapshot))
	return c.encode(cursorKindDetail, payload)
}

func (c *cursorCodec) decodeDetail(token, routeID string, currentLength int) (before, snapshot int, err error) {
	payload, err := c.decode(token, cursorKindDetail)
	if err != nil {
		return 0, 0, err
	}
	if len(payload) < 18 {
		return 0, 0, errCursorInvalid
	}
	length := int(binary.BigEndian.Uint16(payload[:2]))
	if length == 0 || 2+length+16 != len(payload) {
		return 0, 0, errCursorInvalid
	}
	id := string(payload[2 : 2+length])
	if !sessions.ValidID(id) || subtle.ConstantTimeCompare([]byte(id), []byte(routeID)) != 1 {
		return 0, 0, errCursorInvalid
	}
	offset := 2 + length
	before64 := binary.BigEndian.Uint64(payload[offset : offset+8])
	snapshot64 := binary.BigEndian.Uint64(payload[offset+8 : offset+16])
	if before64 > math.MaxInt || snapshot64 > math.MaxInt {
		return 0, 0, errCursorInvalid
	}
	before, snapshot = int(before64), int(snapshot64)
	if before < 0 || snapshot < 0 || before > snapshot || snapshot > currentLength {
		return 0, 0, errCursorInvalid
	}
	return before, snapshot, nil
}

func (c *cursorCodec) encode(kind byte, payload []byte) (string, error) {
	body := make([]byte, 0, 18+len(payload)+sha256.Size)
	body = append(body, cursorVersion)
	body = append(body, c.keyID[:]...)
	body = append(body, kind)
	body = append(body, payload...)
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write([]byte("serpe.http.cursor.v1"))
	_, _ = mac.Write(body)
	body = mac.Sum(body)
	token := base64.RawURLEncoding.EncodeToString(body)
	if len(token) > maxCursorWireBytes {
		return "", errCursorInvalid
	}
	return token, nil
}

func (c *cursorCodec) decode(token string, kind byte) ([]byte, error) {
	if token == "" || len(token) > maxCursorWireBytes {
		return nil, errCursorInvalid
	}
	decodedLength := base64.RawURLEncoding.DecodedLen(len(token))
	if decodedLength < 18+sha256.Size || decodedLength > maxCursorWireBytes {
		return nil, errCursorInvalid
	}
	buffer := make([]byte, decodedLength)
	n, err := base64.RawURLEncoding.Decode(buffer, []byte(token))
	if err != nil || n != decodedLength {
		return nil, errCursorInvalid
	}
	buffer = buffer[:n]
	if buffer[0] != cursorVersion {
		return nil, errCursorInvalid
	}
	if subtle.ConstantTimeCompare(buffer[1:17], c.keyID[:]) != 1 {
		return nil, errCursorStale
	}
	body, gotMAC := buffer[:len(buffer)-sha256.Size], buffer[len(buffer)-sha256.Size:]
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write([]byte("serpe.http.cursor.v1"))
	_, _ = mac.Write(body)
	wantMAC := mac.Sum(nil)
	if subtle.ConstantTimeCompare(gotMAC, wantMAC) != 1 {
		return nil, errCursorInvalid
	}
	if body[17] != kind {
		return nil, errCursorInvalid
	}
	return append([]byte(nil), body[18:]...), nil
}

func cursorMessage(err error) string {
	if errors.Is(err, errCursorStale) {
		return "stale_cursor"
	}
	return "invalid_cursor"
}
