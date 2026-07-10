// Package codec is the sole translation layer between canonical and wire. It
// fully encapsulates protocol differences: the agent and provider see only
// canon.*, while HTTP sees only protocol-specific JSON / SSE.
package codec

import (
	"io"
	"net/http"

	"github.com/tw8ap/ouro/internal/canon"
)

// Codec translates bidirectionally between canonical and one wire protocol.
//
// encode and decode are inverse operations on the same wire schema. The agent's
// HTTP provider uses EncodeRequest/DecodeResponse/DecodeStream. The reverse
// operations stay in the interface so tests can prove round-trip fidelity and
// protocol support remains symmetrical.
type Codec interface {
	// Name identifies the protocol:
	// "openai-responses" | "openai-chat" | "anthropic-messages".
	Name() string

	// ---- Request (bidirectional wire schema) ----

	// EncodeRequest encodes a canonical Request into an outbound wire body
	// (client direction).
	EncodeRequest(*canon.Request) ([]byte, error)
	// DecodeRequest parses a wire request body into a canonical Request.
	// Inverse of EncodeRequest.
	DecodeRequest([]byte) (*canon.Request, error)

	// ---- Response (bidirectional wire schema) ----

	// DecodeResponse parses an inbound wire body into a canonical Response
	// (client direction).
	DecodeResponse([]byte) (*canon.Response, error)
	// EncodeResponse renders a canonical Response into a wire response body.
	// Inverse of DecodeResponse on the expressible fields; per-protocol lossy
	// downgrades documented in the protocol docs excepted.
	EncodeResponse(*canon.Response) ([]byte, error)
	// EncodeError renders a Go error into a protocol-native error body and
	// returns the HTTP status code to write.
	EncodeError(status int, err error) ([]byte, int)

	// ---- Streaming ----

	// DecodeStream reads upstream SSE from r and emits a canonical Event stream
	// (client direction). The channel closes on normal end; an ErrorEvent
	// carries an in-stream error.
	DecodeStream(r io.Reader) (<-chan canon.Event, error)
	// EncodeStream consumes a canonical Event stream and writes wire SSE to w.
	// Inverse of DecodeStream in event semantics (SSE frame formats differ).
	EncodeStream(w io.Writer, events <-chan canon.Event) error
}

// Protocol bundles a Codec with its HTTP envelope (endpoint path, auth headers)
// so HTTPProvider can call an upstream API without protocol-specific branches.
type Protocol struct {
	Name           string // same as Codec.Name
	DefaultBaseURL string // API version root used when the caller omits baseURL
	Endpoint       string // path appended to the API version root, e.g. "/messages"
	Auth           func(apiKey string) http.Header
	Codec          Codec
}
