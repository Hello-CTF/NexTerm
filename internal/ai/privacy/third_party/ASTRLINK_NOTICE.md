`ASTRLINK-LICENSE` contains the Apache License 2.0 text under which AstrLink and the ported Hugging Face components below are licensed. License files are named as shipped in the privacy runtime's `licenses/` directory; in the NexTerm source tree they are `internal/ai/privacy/third_party/ASTRLINK_LICENSE`, `ONNX_RUNTIME_LICENSE` and `ONNX_RUNTIME_<version>_NOTICES`.

The following files are adapted from [Calcium-Ion/AstrLink](https://github.com/Calcium-Ion/AstrLink), commit `24ddf0d823785fe9b3ed7ee23b3a0fc517313e93`:

- `restore_forms.go` and `restore_index.go`: copied from `core/internal/privacy`, retaining its syntax-aware exact, bare, HTML, JSON-escaped and percent-encoded placeholder matching and prefix index. `restoreFrom` adds an already-emitted left-boundary offset for stream chunks, retaining word-boundary and backslash-parity guards across chunks.
- `restore_forms_test.go` and `restore_index_test.go`: upstream restoration contract tests, excluding the natural-placeholder allocator test and adapting the allocator fixture to the token-only adapter.
- `structured_json.go`: literal scanner and span alignment from `core/internal/privacy/structured_json.go`, with `Span` replacing upstream `Finding`. Numeric findings expand to complete JSON literals; UTF-16 surrogate pairs are kept together when aligning escaped string findings. The NexTerm adapter uses a separate HMAC domain for numeric markers to retain number/string types during restoration; sensitive structure keys fail closed.
- `json_parse.go`: copied from `core/internal/privacy`, retaining duplicate-key detection and bounded-depth parsing.
- `placeholders.go`: the token-only portion of `core/internal/privacy/placeholders.go`, the token names from `document.go`, and the minimal redaction types from `types.go`. Natural stand-ins and their storage ledger are not included. NexTerm supplies an account-and-conversation-specific key and caches each allocation within the request. Placeholder suppression is restricted to the exact generated suffixed formats.

NexTerm's `Exchange` adapter calls these implementations over decoded provider messages. Each response keeps only the current request's in-memory restoration map. Stable keyed placeholders follow AstrLink's prefix-cache design, with additional account and conversation key separation.

No RelayKit, gateway, AstrLink account/storage layer, or AGPL dependency is incorporated by these extracted files.

## Local inference engine

`../inference` is a Go port of AstrLink privacy-worker at commit `24ddf0d823785fe9b3ed7ee23b3a0fc517313e93` ([apps/privacy-worker](https://github.com/Calcium-Ion/AstrLink/tree/24ddf0d823785fe9b3ed7ee23b3a0fc517313e93/apps/privacy-worker), Apache-2.0):

- `manifest.go`: the installation-to-model-manifest adapter and the manifest checks that apply to it.
- `detector.go`: sliding windows, nearest-to-centre window stitching, the request token limit and span ordering from `engine.rs`.
- `decoder.go` and `offset_contract.go`: the HF token-classification BIO/BIOES decoder and the `bio-offset-consistency-v1` contract (AstrLink Guard `tools/offset-worker/contract.rs`).
- `span_contract.go`, `span_patterns.txt` (byte-identical upstream generator output) and `span_unicode.go`: the `existing-ip-and-dynamic-url-v1` span contract (AstrLink Guard `tools/span-worker/refine.rs`). The Rust `regex` patterns are translated to RE2 at load: nested classes are flattened, `\s` becomes Unicode White_Space and `\uXXXX` becomes `\x{XXXX}`; any other construct is rejected.
- `testdata/`: upstream code-privacy evaluation cases and the synthetic span-contract oracle.
- Tests port the upstream decoder, contract, stitching and manifest tests.

NexTerm adaptations: the engine runs in-process and calls the bundled ONNX Runtime library through its C API, loaded at run time with [purego](https://github.com/ebitengine/purego) (Apache-2.0) instead of cgo (`ort_api.go` declares the used enum values and OrtApi slots of the MIT-licensed `onnxruntime_c_api.h` v1.23.2); ONNX Runtime logging is limited to fatal messages instead of passing through a logging callback; on macOS the intra-op pool uses at most half the cores, up to four, instead of lowering the QoS of pool threads created through custom thread callbacks; cancellation is checked periodically while tokenizing and between windows and also terminates a running window; errors carry no user text. Only the HF token-classification adapter that NexTerm installations produce is ported; the OpenAI, PPLX and sensitive-guard adapters, Core ML and DirectML providers, the worker protocol and the CI synthetic-model guard are not included.

The tokenizer (`tokenizer.go`, `normalized.go`, `precompiled.go`, `pretokenize.go`, `unigram.go`) is a Go port of the subset of [Hugging Face tokenizers](https://github.com/huggingface/tokenizers) 0.23.1 and [spm_precompiled](https://github.com/huggingface/spm_precompiled) 0.1.4 that the AstrLink Guard tokenizer uses, both Apache-2.0 (`ASTRLINK-LICENSE` carries the same Apache License 2.0 text). Unsupported tokenizer components fail at load. The Oniguruma pre-tokenizer pattern `(?<![0-9\s])\s+|^\s+` is implemented directly because RE2 has no lookbehind. `grapheme.go` ports the extended grapheme cluster rules and `grapheme_tables.go` is generated from the tables of [unicode-segmentation](https://github.com/unicode-rs/unicode-segmentation) 1.13.3 (Unicode 17.0.0, MIT OR Apache-2.0, used here under Apache-2.0) so precompiled normalization sees the same grapheme clusters. Each ported file names its upstream source and states that it was modified for NexTerm.

No model weights are included. `ONNX-RUNTIME-LICENSE` and `ONNX-RUNTIME-THIRD-PARTY-NOTICES` are copied from the v1.23.0 (iOS) and v1.23.2 (desktop and Android) tags of microsoft/onnxruntime for the bundled runtime library.
