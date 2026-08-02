# ouro

`ouro` is a provider-neutral Go library for invoking language models through a
small canonical request, response, and streaming API.

Supported protocols:

- OpenAI Responses
- OpenAI Chat Completions
- Anthropic Messages
- Gemini GenerateContent

Each protocol can run on either the **default** built-in HTTP/JSON/SSE Driver
or the corresponding **official vendor Go SDK** Driver. Callers always use
`models.Request`, `models.Response`, `models.Stream`, and `models.Error`; SDK
types never appear in the public API.

## Requirements

- Go 1.26.5 or newer
- An API key for the provider you use

## Install

```sh
go get github.com/h2cone/ouro
```

The root module pins the official OpenAI, Anthropic, and Google Gen AI SDKs so
`Config.Driver = providers.DriverOfficialSDK` works without blank imports or
build tags. Default-Driver callers still resolve those modules; this is the
cost of config-only switching.

Verified SDK versions (upgrade one at a time and re-run the full differential
contract suite):

| Vendor | Module | Version |
| --- | --- | --- |
| OpenAI | `github.com/openai/openai-go/v3` | v3.49.0 |
| Anthropic | `github.com/anthropics/anthropic-sdk-go` | v1.61.0 |
| Google Gen AI | `google.golang.org/genai` | v1.66.0 |

## Quick start (default Driver)

Omitting `Driver` (or setting `DriverDefault`) uses the library's native
HTTP/JSON/SSE adapters. Existing call sites need no migration:

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

## Official SDK Driver

Set `Driver: providers.DriverOfficialSDK` to route Complete and Stream through
the vendor SDK for the selected protocol. There is **no automatic fallback** to
the default Driver if the SDK fails:

```go
provider, err := providers.New(providers.Config{
	Protocol: providers.AnthropicMessages,
	Driver:   providers.DriverOfficialSDK,
	APIKey:   apiKey,
})
```

`Protocol` already selects the vendor; a single `DriverOfficialSDK` value covers
OpenAI, Anthropic, and Gemini. Bound models are immutable: Complete and Stream
always use the Driver chosen at `providers.New`.

Configuration never reads environment variables automatically for either Driver
(including `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, and base URL
env vars). Pass an API key or a custom authenticator explicitly. Official SDK
default retries are disabled so generation POSTs still make exactly one HTTP
attempt unless the caller implements retry outside this package.

`BaseURL` may be either an origin or an API path prefix; for example, both
`https://api.openai.com` and `https://api.openai.com/v1` are accepted without
producing a duplicated `/v1`.

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
