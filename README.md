# ouro

An Agent Harness in Go. The harness owns the loop around the model: assemble
context, stream a response, execute tool calls, append their results, and
continue until done. Provider specifics stay behind a `models.Model` seam.

## Dependency graph

```
                      main.go
                         │
              ┌──────────┴───────────────┐
              │          │               │
              agent ──► core/models ◄─── core/providers
              │         │     ▲          │
              │         │     │          │
              │         │core/sessions   │
              │         │                │
              ▼         ▼                │
             internal/jsonvalue ◄────────┘
```

`agent` never imports `core/providers`; both converge on `core/models`.
`internal/jsonvalue` is a leaf shared by `core/models`, `agent`, and the
providers' `internal/shared`.

## Modules

### `core/models`

```
core/models ──► internal/jsonvalue
├─ Request / Response / Event (streaming)
├─ Tool / ToolCall / ToolChoice
├─ Model (invocation interface)
└─ Content: Validate · CanonicalBytes · Equal
```

### `core/providers`

```
core/providers
└─ internal/
   ├─ driver/{builtin, official}           ─┐
   ├─ protocol/{anthropic, google, openai} ─┤
   ├─ shared ──► internal/jsonvalue        ─┤
   └─ transport/{httpx, sse, sdkhttp}      ─┤
                                            ▼
                                            core/models
```

### `agent`

```
agent ──► core/models
  │
  └──► internal/jsonvalue
├─ Runner: Run · Stream
├─ Tool / ToolResult / Limits
├─ Event: run_start · model_start · model_event · model_end
          · tool_start · tool_end · run_end
└─ outcome: err == nil && Result.Completed()  committable
            budget/stall stops                nil err + StopReason
            failure                           sentinel / *models.Error
                                              / ctx err + partial Result
```

### `core/sessions`

```
core/sessions ──► core/models
├─ Session
├─ Store ◄── MemoryStore
└─ Manager: Create · Get · Fork · Update · Append · Delete
```

Minimal use:

```go
manager, _ := sessions.NewManager(sessions.NewMemoryStore())
created, _ := manager.Create(ctx, sessions.New("sess-1", "/work"))
_, _ = manager.Append(ctx, created.ID,
    models.NewUserMessage(models.Text("What is in this repo?")),
    models.NewAssistantMessage(models.Text("Let me look.")),
)
```

### `main.go`

```
main.go ─┬─► agent
         ├─► core/models
         └─► core/providers
```

Wiring and rendering only; the model-tool loop lives in package `agent`.

## License

MIT
