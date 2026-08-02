package responses

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

var responsesUnaryFixture = []byte(`{"id":"r","model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}]}`)

const responsesStreamFixture = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"m\",\"status\":\"in_progress\"}}\n\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"m\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}]}}\n\n"

func BenchmarkOpenAIResponsesUnaryDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var wire responseWire
		if err := json.Unmarshal(responsesUnaryFixture, &wire); err != nil {
			b.Fatal(err)
		}
		if _, err := decodeResponse(wire, "", 4096); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOpenAIResponsesStreamDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		source := NewSSEStreamSource(sse.NewReader(io.NopCloser(strings.NewReader(responsesStreamFixture)), 4096), "", "m", false, 4096)
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
