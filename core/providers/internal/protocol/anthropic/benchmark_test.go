package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

var anthropicUnaryFixture = []byte(`{"id":"r","model":"m","role":"assistant","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)

const anthropicStreamFixture = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"r\",\"model\":\"m\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"

func BenchmarkAnthropicMessagesUnaryDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var wire messageWire
		if err := json.Unmarshal(anthropicUnaryFixture, &wire); err != nil {
			b.Fatal(err)
		}
		if _, err := decodeResponse(wire, "", 4096); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnthropicMessagesStreamDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		source := NewSSEStreamSource(sse.NewReader(io.NopCloser(strings.NewReader(anthropicStreamFixture)), 4096), "", "m", false, 4096)
		stream := models.NewStream(context.Background(), source)
		for stream.Next() {
		}
		if stream.Err() != nil {
			b.Fatal(stream.Err())
		}
	}
}

func BenchmarkRequestEncode(b *testing.B) {
	for _, tools := range []bool{false, true} {
		name := "text"
		if tools {
			name = "tools"
		}
		b.Run(name, func(b *testing.B) {
			req := models.NewTextRequest("hello")
			req.Generation.MaxOutputTokens = models.Some(128)
			if tools {
				req.Tools = []models.Tool{models.NewTool("lookup", "", json.RawMessage(`{"type":"object"}`))}
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := EncodeRequest("m", req, false, false, 1<<20); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
