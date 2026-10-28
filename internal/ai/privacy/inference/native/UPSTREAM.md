AstrLink privacy-worker sources are from:

https://github.com/Calcium-Ion/AstrLink/tree/24ddf0d823785fe9b3ed7ee23b3a0fc517313e93/apps/privacy-worker

The original Apache-2.0 license is in LICENSE-ASTRLINK. Files under src other
than bridge.rs and lib.rs, and testdata, retain upstream implementations.

NexTerm adaptations:

- Replace the executable entry point with an in-process, versioned C ABI.
- Initialize ONNX Runtime from the application's explicit bundled library path.
- Adapt the verified download installation manifest to the upstream model manifest
  in memory, without modifying downloaded files.
- Suppress panic logging and return bounded errors without user text.
- Check cancellation between texts and inference windows.
- Build a shared/static native library for desktop, server, Android and iOS.
- Keep the original HF tokenizer, sliding-window stitching, BIO decoder and
  IP/URL span contracts. Confidence filtering remains the caller's policy.

No model weights are included. Cargo.lock records native dependency versions.

Bundled ONNX Runtime license/notices are copied from the corresponding
v1.23.0 (iOS) and v1.23.2 (desktop/Android) tags of microsoft/onnxruntime.
The runtime preparation script also bundles every native Cargo dependency's
available LICENSE/COPYING/NOTICE files and a machine-readable license inventory.
