This directory's `ASTRLINK_LICENSE` contains AstrLink's Apache-2.0 license.

The following files are adapted from [Calcium-Ion/AstrLink](https://github.com/Calcium-Ion/AstrLink), commit `24ddf0d823785fe9b3ed7ee23b3a0fc517313e93`:

- `restore_forms.go` and `restore_index.go`: copied from `core/internal/privacy`, retaining its syntax-aware exact, bare, HTML, JSON-escaped and percent-encoded placeholder matching and prefix index. `restoreFrom` adds an already-emitted left-boundary offset for stream chunks, retaining word-boundary and backslash-parity guards across chunks.
- `restore_forms_test.go` and `restore_index_test.go`: upstream restoration contract tests, excluding the natural-placeholder allocator test and adapting the allocator fixture to the token-only adapter.
- `structured_json.go`: literal scanner and span alignment from `core/internal/privacy/structured_json.go`, with `Span` replacing upstream `Finding`. Numeric findings expand to complete JSON literals; UTF-16 surrogate pairs are kept together when aligning escaped string findings. The NexTerm adapter uses a separate HMAC domain for numeric markers to retain number/string types during restoration; sensitive structure keys fail closed.
- `json_parse.go`: copied from `core/internal/privacy`, retaining duplicate-key detection and bounded-depth parsing.
- `placeholders.go`: the token-only portion of `core/internal/privacy/placeholders.go`, the token names from `document.go`, and the minimal redaction types from `types.go`. Natural stand-ins and their storage ledger are not included. NexTerm supplies an account-and-conversation-specific key and caches each allocation within the request. Placeholder suppression is restricted to the exact generated suffixed formats.

NexTerm's `Exchange` adapter calls these implementations over decoded provider messages. Each response keeps only the current request's in-memory restoration map. Stable keyed placeholders follow AstrLink's prefix-cache design, with additional account and conversation key separation.

No RelayKit, gateway, AstrLink account/storage layer, or AGPL dependency is incorporated by these extracted files.
