package google

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

var geminiUnaryFixture = []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"modelVersion":"m","responseId":"r"}`)

const geminiStreamFixture = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hello\"}]},\"finishReason\":\"STOP\"}],\"modelVersion\":\"m\",\"responseId\":\"r\"}\n\n"

func BenchmarkGeminiGenerateContentUnaryDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var wire responseWire
		if err := json.Unmarshal(geminiUnaryFixture, &wire); err != nil {
			b.Fatal(err)
		}
		if _, err := decodeResponse(wire, "", "m", 4096); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGeminiGenerateContentStreamDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		source := NewSSEStreamSource(sse.NewReader(io.NopCloser(strings.NewReader(geminiStreamFixture)), 4096), "", "m", 4096)
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
				if _, err := EncodeRequest(req, false, 1<<20); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
