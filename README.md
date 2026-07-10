# ouro

Extensible local coding-agent runtime built around canonical LLM types. The CLI
is the first shell adapter, not the boundary of the Agent: TUI and Web UI shells
can reuse the same runtime, sessions, tools, and upstream providers.

## Features

- Shell-neutral Agent loop and multi-turn `Session` API
- Local CLI shell in `cmd/agent`
- Multi-protocol upstream support for OpenAI Responses, OpenAI Chat Completions, and Anthropic Messages
- Standard-library HTTP client and JSON handling
- Built-in local tools for shell execution plus file reads and writes
- Focused tests for session isolation, tool execution, response handling, shell parsing, and semantic stop conditions

## Architecture

```text
CLI shell     TUI shell     Web UI shell
    \             |             /
     \            |            /
      +---- agent.Session -----+
                 |
              Agent loop
                 |
        Provider -> Codec -> LLM API
                 |
              local tools
```

Each interactive shell owns its input, rendering, transport, authentication,
and one `agent.Session` per conversation. `Agent` owns only the reusable tool
loop. A completed turn returns both the final response and the full canonical
transcript, including internal tool-use rounds, so shells do not need to infer
or duplicate runtime state.

## Getting Started

### Requirements

- Go 1.22 or newer
- An upstream API key environment variable: `OPENAI_API_KEY` for OpenAI protocols or `ANTHROPIC_API_KEY` for Anthropic

### Run the current CLI shell

```powershell
$env:OPENAI_API_KEY="your-api-key"
go run ./cmd/agent -- "gpt-5.4" "hello"
```

Use another upstream protocol:

```powershell
$env:ANTHROPIC_API_KEY="your-api-key"
go run ./cmd/agent -protocol anthropic-messages -- "claude-opus-4-8" "hello"
```

Optional environment variables:

- `OPENAI_BASE_URL` for `openai-responses` and `openai-chat`
- `ANTHROPIC_BASE_URL` for `anthropic-messages`

Command-line flags and arguments:

- `-protocol` selects `openai-responses`, `openai-chat`, or `anthropic-messages`
- `-base-url` overrides the protocol default API version root
- `-api-key` or `-api-key-env` overrides environment-based key lookup
- First positional argument is required `model`
- Remaining positional arguments are required `task`

The module root binary prints a reminder for the currently available shell:

```powershell
go run .
```

## Test

```powershell
go test ./...
```

## License

MIT. See [LICENSE](LICENSE).
