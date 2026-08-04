# ouro

Ouro is my attempt to build an Agent Harness in Go.

The harness owns the loop around the model: assemble context, stream a response,
execute tool calls, append their results, and continue until the task is done.
Provider-specific details stay behind a small, shared model API.

## Design

The module is layered so that the agent loop never depends on any provider. A
`models.Model` is the only seam between them: providers produce one, the agent
consumes it.

- **`core/models`** — provider-neutral requests, responses, streaming events,
  tools, and the `Model` invocation interface. The foundation everything else
  builds on; depends only on the standard library and `internal/jsonvalue`.
- **`core/providers`** — a static catalog mapping a wire `Protocol` (OpenAI
  Chat/Responses, Anthropic Messages, Google GenerateContent) to a concrete
  driver, selected via `Driver` between a hand-written HTTP driver and the
  vendor's official SDK. Internals are split by concern:
  - `internal/protocol/*` — request/response codecs per vendor;
  - `internal/transport/*` — HTTP, SSE, and SDK transports;
  - `internal/driver/{builtin,official}` — the two driver families.
- **`agent`** — the reusable runtime: `Runner`, tools, limits, and run-level
  events over a pull-based `Stream`. Depends only on `core/models`.
- **`main.go`** — a thin CLI that assembles a provider model and a `Runner`,
  then renders events. It holds no logic of its own.

## Dependency graph

```
                      main.go
                     ╱        ╲
                 agent      core/providers
                    │             │
                    │        internal/
                    │        ├ driver/{builtin, official}
                    │        ├ protocol/{anthropic, google, openai}
                    │        └ transport/{httpx, sse, sdkhttp}
                    │             │
                    └──────► core/models ◄──────┘
                               │
                               ▼
                        internal/jsonvalue
```

Both `agent` and the provider stack converge on `core/models`; `agent` never
imports `core/providers`. `internal/jsonvalue` is a small leaf shared by
`core/models` and `agent`.

## License

MIT
