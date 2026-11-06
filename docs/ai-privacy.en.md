# AI privacy redaction

**English** | [中文](ai-privacy.md) · [Back to README](../README_EN.md)

Off by default. In Settings → AI privacy redaction (AI 信息脱敏), download the latest released `QuantumNous/astrlink-guard` model (downloaded from Hugging Face; weights are not bundled), select INT8, FP32, or FP16, then enable it.

- When enabled, an unavailable model or runtime blocks requests instead of sending plaintext; images and other non-text content are not redacted and go to the AI provider unchanged; detection can miss sensitive information.
- Placeholders stay stable within a conversation, and responses and tool arguments are restored automatically.
- Local inference needs Linux (glibc 2.27 or newer with libstdc++; musl is not supported), Windows, macOS 13.4+, iOS/iPadOS 15.1+, or Android 7.0+. Redaction is unavailable on platforms ONNX Runtime no longer publishes (currently Intel Macs); the rest of the app works.
- Detection runs on the device running AI, or on the server in browser mode.

The implementation reuses [AstrLink](https://github.com/Calcium-Ion/AstrLink); see the [source and license notice](../internal/ai/privacy/third_party/ASTRLINK_NOTICE.md).
