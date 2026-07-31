package providers_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers"
)

func ExampleNew() {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"r1","model":"example-model","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`)
	}))
	defer server.Close()

	provider, err := providers.New(providers.Config{
		Protocol:   providers.OpenAIResponses,
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		panic(err)
	}
	model, err := provider.Model("example-model")
	if err != nil {
		panic(err)
	}
	response, err := model.Complete(context.Background(), models.NewTextRequest("say hello"))
	if err != nil {
		panic(err)
	}
	fmt.Println(response.Text())
	// Output: hello
}

func ExampleProvider_stream() {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"id\":\"r1\",\"model\":\"example-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"r1\",\"model\":\"example-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider, _ := providers.New(providers.Config{Protocol: providers.OpenAIChatCompletions, BaseURL: server.URL, HTTPClient: server.Client()})
	model, _ := provider.Model("example-model")
	stream, err := model.Stream(context.Background(), models.NewTextRequest("say hello"))
	if err != nil {
		panic(err)
	}
	defer stream.Close()
	for stream.Next() {
		fmt.Print(stream.Text())
	}
	if err := stream.Err(); err != nil {
		panic(err)
	}
	fmt.Printf(" (%s)\n", stream.Response().Candidates[0].FinishReason)
	// Output: hello (stop)
}
