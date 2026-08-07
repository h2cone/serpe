# ouro

An Agent Harness in Go. The harness owns the loop around the model: assemble
context, stream a response, execute tool calls, append their results, and
continue until done. Provider specifics stay behind a `models.Model` seam.

## Dependency graph

```
                      main.go          cmd/ouroserve
                         │                   │
              ┌──────────┴───────────────┐   │
              │          │               │   ▼
              agent ──► core/models ◄─── core/providers
              │         │     ▲          │   │
              │         │     │          │   ▼
              │         │core/sessions   │ server ──► compose
              │         │                │   │           │
              ▼         ▼                │   └───────────┘
             internal/jsonvalue ◄────────┘
              ▲
              │
           compose ──► agent
              │
              └──► core/sessions

  ui/ (React Router 7) ──HTTP/SSE──► server/
```

`agent` never imports `core/sessions` or `core/providers`. `compose` is the
application seam that joins `agent.Runner` with `sessions.Manager` for a
single turn boundary. `server` exposes REST + SSE over `compose`/`sessions`.
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
├─ Tool / ToolOutput / Limits
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
│         ◄── FileStore   (disk: <root>/<id>.json, temp+rename)
└─ Manager: Create · Get · List · Fork · Update · Append · AppendAt · Delete
```


Minimal use (in-process):

```go
manager, _ := sessions.NewManager(sessions.NewMemoryStore())
created, _ := manager.Create(ctx, sessions.New("sess-1", "/work"))
_, _ = manager.Append(ctx, created.ID,
    models.NewUserMessage(models.Text("What is in this repo?")),
    models.NewAssistantMessage(models.Text("Let me look.")),
)
```

Disk-backed store (process restart recovery; single process):

```go
store, _ := sessions.NewFileStore("/var/lib/ouro/sessions")
manager, _ := sessions.NewManager(store)
// Session IDs must be filesystem-safe: [A-Za-z0-9._-], not Windows device names.
```

### `compose`

```
compose ──► agent
       ──► core/sessions
       ──► core/models
├─ TurnService: Send · Stream
└─ Turn (stream decorator): Next · Event · Err · Result · Session
```

Turn boundary: Get → build request → Run/Stream → commit suffix only when
`err == nil && result.Completed()`, via `Manager.AppendAt` (optimistic length
CAS; `ErrConcurrentTurn` / `sessions.ErrConflict` on conflict). After stream
exhaust, `Turn.Err()` is the singular terminal check (includes commit failure).
Close never commits.

```go
svc, _ := compose.New(compose.Config{Runner: runner, Manager: manager})
result, committed, err := svc.Send(ctx, "sess-1", "What is in this repo?")
// or: turn, _ := svc.Stream(ctx, "sess-1", prompt); for turn.Next() { ... }
```

### `server` + `cmd/ouroserve`

```
server ──► compose · agent · core/sessions · core/models
  GET  /api/health
  CRUD /api/sessions[/{id}[/fork]]
  POST /api/runs  → text/event-stream (TurnService.Stream)
```

```bash
# API (default MemoryStore; set OURO_SESSIONS_DIR for FileStore)
go run ./cmd/ouroserve   # :8080

# UI (dev; proxies /api → :8080)
cd ui && npm install && npm run dev
```

### `main.go`

```
main.go ─┬─► agent
         ├─► core/models
         └─► core/providers
```

CLI wiring and rendering only; the model-tool loop lives in package `agent`.

## License

MIT
