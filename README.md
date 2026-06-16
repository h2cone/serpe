# ouro

Minimal Go agent example built around the OpenAI Responses API.

## Features

- Small command-line agent in `cmd/agent`
- Standard-library HTTP client and JSON handling
- Built-in local tools for shell execution plus file reads and writes
- Focused tests for tool execution, response handling, CLI parsing, and semantic stop conditions

## Getting Started

### Requirements

- Go 1.22 or newer
- An `OPENAI_API_KEY` environment variable

### Run the example agent

```powershell
$env:OPENAI_API_KEY="your-api-key"
go run ./cmd/agent -- "gpt-5.4" "hello"
```

Optional environment variables:

- `OPENAI_BASE_URL` to point at a compatible API base URL

Command-line arguments:

- First positional argument is required `model`
- Remaining positional arguments are required `task`

The module root binary prints a reminder for the agent entrypoint:

```powershell
go run .
```

## Test

```powershell
go test ./...
```

## License

MIT. See [LICENSE](LICENSE).
