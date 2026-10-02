# Go skeleton ownership and extension contracts

This module is a foundation, not a second set of business implementations. Its contracts are versioned and self-consistent within the Go implementation; Rust data/wire compatibility and differential verification are not required.

## Package ownership

| Path | Owner / responsibility |
| --- | --- |
| `cmd/nexterm-desktop` | Wails-only composition and window/assets adapter. No domain rules. |
| `cmd/nexterm-server` | Server CLI composition. It does not import Wails and builds with `CGO_ENABLED=0`. |
| `internal/app` | Shared composition root, lifecycle, CLI shapes, health/server loop, and token-command stdout contract. Future shared modules are supplied through `Config.Modules`, not registered separately in each binary. |
| `internal/ipc` | Stable wire DTOs, command registry/dispatcher, errors, named events, RPC handler, and stream contracts. No storage, terminal, AI, Docker, or sync rules. |
| `internal/platform` | Desktop/server paths, logging setup, and process signals. Domain-specific DPAPI, Keychain, PTY, mount, and firewall code belongs to the corresponding domain adapters. |
| `internal/version` | Linker-injected `Version` and `Commit` values. Build tasks own the `-ldflags -X` values. |

## Command and DTO contract

- Register a flat-argument command with `ipc.Register[In, Out](dispatcher, name, handler)`. Register a command whose request is `{args:{args:{...}}}` with `ipc.RegisterNested`.
- A handler receives `context.Context`, `*ipc.Call`, and its typed input. `Call` exposes the raw arguments for tri-state DTOs, the channel reference, stable client ID, event emitter, and stream factory.
- `RegisterRaw` is reserved for commands that must control decoding, such as explicit-null versus omitted fields. Command names are registered once; duplicates and unknown commands fail explicitly.
- DTOs use consistent camelCase `encoding/json` tags. Response success is `{ok:true,data:...}`, including `data:null`; command failure is `{ok:false,error:{code,message,detail?}}`. HTTP command failures remain HTTP 200.
- Keep `[]byte` out of ordinary DTOs unless base64 is the intended wire format. PTY and Docker output use `BinaryStream`; AI jobs use `OpenTypedJSONStream[AiEvent]` with the AI domain's concrete event type.
- Events use the `Topic*` constants. `ipc.Event` is the HTTP/WS `{event,payload}` envelope; the Wails adapter emits the topic and payload separately.

## Lifecycle and server extension points

- Components implement `Start(context.Context)` and `Shutdown(context.Context)`. `Application` starts them in order, rolls back partial startup, and shuts them down in reverse order.
- `Module.RegisterCommands` centralizes each domain's registration; `Module.Component` attaches its shared lifecycle. Desktop and server must receive the same module composition, with capability-specific behavior decided inside shared services rather than duplicated handlers.
- `Application.Serve` mounts `/healthz` and, unless sync-only, `/rpc`. `ServeConfig.SyncRPC` is the injection point for the authenticated, restricted sync dispatcher; `ServeConfig.Static` is the future web/static adapter. Do not expose the unrestricted dispatcher as `/sync/rpc`.
- `RunTokenCommand` is the only token-output path. A future store implements `TokenStore`; token commands print exactly one token plus newline to stdout and do not initialize logging.

## Stream adapter requirements

Wails and HTTP/WS adapters must preserve per-channel ordering, cancellation, close idempotency, backpressure, and reconnect semantics. A `BinaryStream` sends bytes without base64 JSON conversion. A `JSONStream` sends one ordered JSON value per frame; `TypedJSONStream` only adds type-safe marshaling. The current factory stubs return `ErrStreamsUnavailable` until those adapters land.

## Build boundary

`go.mod` pins Go 1.26.8 through the toolchain directive and Wails v3.0.0-alpha.98. The server and Windows desktop are zero-cgo. Only the Wails macOS/Linux desktop platform layer uses cgo. Every release artifact must be measured against the Rust size baseline; skeleton measurements alone do not prove the final requirement.
