package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tw8ap/ouro/internal/canon"
	"github.com/tw8ap/ouro/internal/codec"
	"github.com/tw8ap/ouro/internal/codec/openai"
)

func TestHTTPProviderCompleteRoundTrip(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("auth = %q", got)
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1","object":"response","model":"gpt-5.4",
			"output":[{"type":"function_call","call_id":"call_1","name":"write_file","arguments":"{\"path\":\"a.txt\",\"content\":\"hi\"}"}],
			"status":"completed",
			"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}
		}`))
	}))
	defer srv.Close()

	proto := codec.Protocol{
		Name:           "openai-responses",
		DefaultBaseURL: "",
		Endpoint:       "/responses",
		Auth:           codec.OpenAIAuth,
		Codec:          openai.ResponsesCodec{},
	}
	p := NewHTTPProvider(proto, srv.URL+"/v1", "test-key", nil)

	resp, err := p.Complete(context.Background(), &canon.Request{
		Model: "gpt-5.4",
		Conversation: canon.Conversation{Messages: []canon.Message{{
			Role: canon.RoleUser, Content: []canon.ContentBlock{&canon.TextBlock{Text: "hi"}},
		}}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.ID != "resp_1" || resp.FinishReason != canon.FinishToolCalls {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("content len = %d", len(resp.Content))
	}
	if gotBody["model"] != "gpt-5.4" {
		t.Fatalf("sent model = %v", gotBody["model"])
	}
	if gotBody["store"] != false {
		t.Fatalf("store should be false, got %v", gotBody["store"])
	}
}

func TestHTTPProviderStreamRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) { _, _ = w.Write([]byte(s)); fl.Flush() }
		write("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_2\",\"model\":\"gpt-5.4\"}}\n\n")
		write("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		write("event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n")
		write("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n")
		write("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
	}))
	defer srv.Close()

	proto := codec.Protocol{
		Name:     "openai-responses",
		Endpoint: "/responses",
		Auth:     codec.OpenAIAuth,
		Codec:    openai.ResponsesCodec{},
	}
	p := NewHTTPProvider(proto, srv.URL+"/v1", "test-key", nil)

	events, err := p.Stream(context.Background(), &canon.Request{
		Model: "gpt-5.4",
		Conversation: canon.Conversation{Messages: []canon.Message{{
			Role: canon.RoleUser, Content: []canon.ContentBlock{&canon.TextBlock{Text: "hi"}},
		}}},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	resp, err := canon.Assemble(events)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if resp.ID != "resp_2" {
		t.Fatalf("id = %q", resp.ID)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("content len = %d", len(resp.Content))
	}
	if tb, ok := resp.Content[0].(*canon.TextBlock); !ok || tb.Text != "hi" {
		t.Fatalf("text = %#v", resp.Content[0])
	}
	if resp.Usage.OutputTokens != 1 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestHTTPProviderStreamCancellationDrainsCodecEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	saturated := make(chan struct{})
	producerDone := make(chan struct{})
	streamCodec := &saturatingStreamCodec{
		saturated:    saturated,
		producerDone: producerDone,
	}
	p := NewHTTPProvider(codec.Protocol{
		Name:     streamCodec.Name(),
		Endpoint: "/stream",
		Auth:     func(string) http.Header { return http.Header{} },
		Codec:    streamCodec,
	}, srv.URL, "", nil)

	ctx, cancel := context.WithCancel(context.Background())
	events, err := p.Stream(ctx, &canon.Request{Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-saturated:
	case <-time.After(time.Second):
		t.Fatal("codec producer did not saturate its event buffer")
	}
	cancel()

	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("codec producer remained blocked after stream cancellation")
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("provider emitted an event after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("provider output channel did not close after cancellation")
	}
}

type saturatingStreamCodec struct {
	saturated    chan struct{}
	producerDone chan struct{}
}

func (*saturatingStreamCodec) Name() string { return "saturating-stream" }

func (*saturatingStreamCodec) EncodeRequest(*canon.Request) ([]byte, error) {
	return []byte(`{}`), nil
}

func (*saturatingStreamCodec) DecodeRequest([]byte) (*canon.Request, error) {
	return &canon.Request{}, nil
}

func (*saturatingStreamCodec) DecodeResponse([]byte) (*canon.Response, error) {
	return &canon.Response{}, nil
}

func (*saturatingStreamCodec) EncodeResponse(*canon.Response) ([]byte, error) {
	return []byte(`{}`), nil
}

func (*saturatingStreamCodec) EncodeError(status int, _ error) ([]byte, int) {
	return []byte(`{}`), status
}

func (c *saturatingStreamCodec) DecodeStream(io.Reader) (<-chan canon.Event, error) {
	events := make(chan canon.Event, 16)
	go func() {
		defer close(events)
		defer close(c.producerDone)
		for i := 0; i < 64; i++ {
			events <- canon.MessageStartEvent{}
			if i == 15 {
				close(c.saturated)
			}
		}
	}()
	return events, nil
}

func (*saturatingStreamCodec) EncodeStream(io.Writer, <-chan canon.Event) error {
	return nil
}
