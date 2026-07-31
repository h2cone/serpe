package sse

import (
	"bytes"
	"io"
	"testing"
)

func FuzzReader(f *testing.F) {
	f.Add([]byte("data: hello\n\n"))
	f.Add([]byte("event: x\r\ndata: one\r\ndata: two\r\n\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		reader := NewReader(bytes.NewReader(data), 4096)
		for count := 0; count < 1024; count++ {
			_, err := reader.Next()
			if err != nil {
				if err != io.EOF {
					_ = err.Error()
				}
				break
			}
		}
		_ = reader.Close()
	})
}
