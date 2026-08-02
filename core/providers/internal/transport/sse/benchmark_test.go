package sse

import (
	"io"
	"strings"
	"testing"
)

func BenchmarkSSEReadTextDelta(b *testing.B) {
	benchmarkRead(b, "event: delta\ndata: {\"type\":\"text\",\"delta\":\"hello\"}\n\n")
}

func BenchmarkSSEReadToolArguments(b *testing.B) {
	benchmarkRead(b, "event: delta\ndata: {\"type\":\"tool\",\"delta\":\"{\\\"city\\\":\\\"Paris\\\"}\"}\n\n")
}

func benchmarkRead(b *testing.B, fixture string) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		reader := NewReader(strings.NewReader(fixture), 4096)
		if _, err := reader.Next(); err != nil {
			b.Fatal(err)
		}
		if _, err := reader.Next(); err != io.EOF {
			b.Fatal(err)
		}
	}
}
