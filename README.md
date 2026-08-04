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
  Chat/Responses, Anthropic Messages, Gemini GenerateContent) to a concrete
  driver, selected via `Driver` between a hand-written HTTP driver and the
  vendor's official SDK. Internals are split by concern:
  - `internal/driver/{builtin,official}` — the two driver families;
  - `internal/protocol/*` — request/response codecs per vendor;
  - `internal/shared` — helpers shared across the provider internals;
  - `internal/transport/*` — HTTP, SSE, and SDK transports.
- **`agent`** — the reusable runtime: `Runner`, tools, limits, and run-level
  events over a pull-based `Stream`. Depends only on `core/models` and the
  module-private `internal/jsonvalue` leaf.
- **`main.go`** — a thin CLI that assembles a provider model and a `Runner`,
  then renders events. It owns wiring and rendering only; the model–tool loop
  lives in package `agent`.

## Dependency graph

```
                      main.go
                   ╱    │     ╲
                  ╱     │      ╲
             agent      │   core/providers
                │       │          │
                │       │     internal/
                │       │     ├ driver/{builtin, official}
                │       │     ├ protocol/{anthropic, google, openai}
                │       │     ├ shared
                │       │     └ transport/{httpx, sse, sdkhttp}
                │       │          │
                └───────┴──► core/models ◄──────┘
                │                  │             │
                ▼                  ▼             │
             internal/jsonvalue ◄────────────────┘
```

Both `agent` and the provider stack converge on `core/models`; `agent` never
imports `core/providers`. `main` also imports `core/models` for request
construction. `internal/jsonvalue` is a small leaf shared by `core/models`,
`agent`, and the providers' `internal/shared` helpers.

## License

MIT
