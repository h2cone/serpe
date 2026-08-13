package sessionwire

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestMessageFragmentMatchesDetailDTO(t *testing.T) {
	message := models.NewUserMessage(
		models.Text("<tag>\n\u2028\x01"),
		models.ImageBytes("image/png", []byte{0, 1, 2, 3, 4}),
		models.ToolCallContent("call-1", "read", json.RawMessage(`{"path":"x"}`)),
		models.ToolResultContent("call-1", "read", true, models.Text("failed")),
	)
	records := make([]models.ContentRecord, len(message.Content))
	for index := range message.Content {
		record, err := models.EncodeContent(message.Content[index])
		if err != nil {
			t.Fatal(err)
		}
		records[index] = record
	}
	wantBuffer := new(bytes.Buffer)
	encoder := json.NewEncoder(wantBuffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		Role    string                 `json:"role"`
		Content []models.ContentRecord `json:"content"`
	}{Role: string(message.Role), Content: records}); err != nil {
		t.Fatal(err)
	}
	want := bytes.TrimSuffix(wantBuffer.Bytes(), []byte{'\n'})
	got, err := EncodeMessageFragment(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("fragment mismatch\n got: %s\nwant: %s", got, want)
	}
	size, err := MessageFragmentSize(message)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(got)) {
		t.Fatalf("size=%d, encoded=%d", size, len(got))
	}
}
