# ouro

`ouro` is a provider-neutral Go library for invoking language models through a
small canonical request, response, and streaming API.

The current adapters support:

- OpenAI Responses
- OpenAI Chat Completions
- Anthropic Messages
- Gemini GenerateContent

## Requirements

- Go 1.26.5 or newer
- An API key for the provider you use

## Install

```sh
go get github.com/h2cone/ouro
```

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers"
)

func main() {
	provider, err := providers.New(providers.Config{
		Protocol: providers.OpenAIResponses,
		APIKey:   os.Getenv("OPENAI_API_KEY"),
	})
	if err != nil {
		panic(err)
	}
	model, err := provider.Model("gpt-5")
	if err != nil {
		panic(err)
	}
	response, err := model.Complete(
		context.Background(),
		models.NewTextRequest("Say hello in one sentence."),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(response.Text())
}
```

Configuration never reads environment variables automatically. Pass an API key
or a custom authenticator explicitly. `BaseURL` may be either an origin or an
API path prefix; for example, both `https://api.openai.com` and
`https://api.openai.com/v1` are accepted without producing a duplicated `/v1`.

Streams use a pull API with one reader. Consume them to completion to obtain the
normalized response:

```go
stream, err := model.Stream(ctx, request)
if err != nil {
	return err
}
defer stream.Close()
for stream.Next() {
	fmt.Print(stream.Text())
}
if err := stream.Err(); err != nil {
	return err
}
response := stream.Response()
```

Tool results carry both the provider call ID and the function name so they can
be mapped across every supported protocol:

```go
result := models.ToolResultContent(call.ID, call.Name, false, models.Text("done"))
```

## Migration from the earlier agent runtime

This revision is a breaking library-focused redesign:

- The module path changed from `github.com/tw8ap/ouro` to
  `github.com/h2cone/ouro`; update imports and run `go mod tidy`.
- The root executable, `cmd/agent`, and the old `internal/agent`,
  `internal/codec`, and `internal/provider` packages were removed. They are not
  compatibility APIs in this revision.
- Use `core/models` for canonical values and interfaces, `core/providers` for
  physical provider adapters, and `core` only when process-wide logical model
  registration is useful.
- Construct tool results with call ID, function name, error status, and result
  content: `ToolResultContent(callID, name, isError, content...)`.

## Test

```sh
go test ./...
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE).
