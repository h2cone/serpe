# ouro

Ouro is my attempt to build an Agent Harness in Go.

The harness owns the loop around the model: assemble context, stream a response,
execute tool calls, append their results, and continue until the task is done.
Provider-specific details stay behind a small, shared model API.

The repository currently contains:

- provider-neutral messages, responses, streams, and tools;
- adapters for OpenAI, Anthropic, and Gemini protocols;
- a minimal tool-calling loop in [`main.go`](main.go).

This is an early project. Its API and structure will change as the harness takes
shape.

## Run

The example requires Go 1.26.5 and an OpenAI-compatible Responses endpoint.

```powershell
$env:OPENAI_API_KEY = "..."
$env:OPENAI_BASE_URL = "https://..."
go run . "What time is it?"
```

## Develop

```sh
go test ./...
go vet ./...
```

## License

MIT
