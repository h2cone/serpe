// Command ouro is a minimal agent loop over the core library: stream the model,
// execute any tool calls, append the results, and repeat until it answers.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers"
)

var tools = []models.Tool{
	models.NewTool("now", "Current wall-clock time in RFC 3339.", json.RawMessage(`{"type":"object","properties":{}}`)),
}

func main() {
	provider := must(providers.New(providers.Config{
		Protocol: providers.OpenAIResponses,
		APIKey:   os.Getenv("OPENAI_API_KEY"),
		BaseURL:  os.Getenv("OPENAI_BASE_URL"),
	}))
	upstreamModel := must(provider.Model("glm-5.2"))

	prompt := "What time is it?"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}
	history := []models.Message{models.NewUserMessage(models.Text(prompt))}

	for {
		stream := must(upstreamModel.Stream(context.Background(), &models.Request{Messages: history, Tools: tools}))
		for stream.Next() {
			fmt.Print(stream.Text())
		}
		must(0, stream.Err())
		_ = stream.Close()
		resp := stream.Response()
		history = append(history, must(resp.AssistantMessage(0)))
		calls := resp.ToolCalls()
		if len(calls) == 0 {
			fmt.Println()
			return
		}
		results := make([]models.Content, 0, len(calls))
		for _, call := range calls {
			results = append(results, models.ToolResultContent(call.ID, call.Name, false, models.Text(time.Now().Format(time.RFC3339))))
		}
		history = append(history, models.NewUserMessage(results...))
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		log.Fatal(err)
	}
	return v
}
