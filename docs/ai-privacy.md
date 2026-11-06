# AI 信息脱敏

**中文** | [English](ai-privacy.en.md) · [返回 README](../README.md)

默认关闭。在「设置 → AI 信息脱敏」下载最新发布的 `QuantumNous/astrlink-guard` 模型（从 Hugging Face 下载，权重不随应用分发），选择 INT8、FP32 或 FP16 后启用。

- 启用后模型或运行时不可用会阻止请求，不会退回明文发送；图片等非文本内容不会脱敏，会原样发送给 AI 服务；识别可能遗漏敏感信息。
- 同一会话内占位稳定，回复和工具参数会自动还原。
- 本地推理需要 Linux（glibc 2.27 或更新并带 libstdc++，不支持 musl）、Windows、macOS 13.4+、iOS/iPadOS 15.1+ 或 Android 7.0+；ONNX Runtime 不再发布的平台（目前为 Intel Mac）上脱敏不可用，应用其余功能不受影响。
- 检测在运行 AI 的设备执行，浏览器模式在服务端执行。

实现复用 [AstrLink](https://github.com/Calcium-Ion/AstrLink)，来源版本和许可证见[声明](../internal/ai/privacy/third_party/ASTRLINK_NOTICE.md)。
