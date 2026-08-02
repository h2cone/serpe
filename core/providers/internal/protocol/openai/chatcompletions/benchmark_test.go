package chatcompletions

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

var chatUnaryFixture = []byte(`{"id":"r","created":1,"model":"m","choices":[{"index":0,"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)

const chatStreamFixture = "data: {\"id\":\"r\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

func BenchmarkOpenAIChatUnaryDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var wire chatResponse
		if err := json.Unmarshal(chatUnaryFixture, &wire); err != nil {
			b.Fatal(err)
		}
		if _, err := decodeResponse(wire, ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOpenAIChatStreamDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		source := NewSSEStreamSource(sse.NewReader(io.NopCloser(strings.NewReader(chatStreamFixture)), 4096), "", "m")
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
				if _, err := EncodeRequest("m", req, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
