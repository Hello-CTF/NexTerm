# M0-T6 吞吐基准（terminal_throughput）

## 结论

| 指标 | 数值 | 目标（§3.2） | 判定 |
|---|---|---|---|
| 终端引擎吞吐（UTF-8 全链路） | **43.4 MB/s** | ≥ 10 MB/s | ✅ 4.3× |
| 终端引擎吞吐（GBK 转码路径） | **39.0 MB/s** | ≥ 10 MB/s | ✅ |
| 100 MB 灌入后环形缓冲驻留 | 33,554,432 字节（= 32 MiB 上限） | 内存不随输入线性增长 | ✅ 精确封顶 |
| 100 MB 灌入后屏幕状态 | 120×40 网格、40 行、无 ANSI 残留 | 状态机合法 | ✅ |

## 测试环境

- OS：Windows 10 (19045) x64，开发机（中端）
- 构建：`cargo bench --bench terminal_throughput`（bench profile，opt-level=3）
- 路径：ConPTY 字节流 → 编码转码（encoding_rs 增量）→ `vt100::Parser`（100k 行滚动）→ `Scrollback`（32 MiB 字节环形缓冲）
- 负载：100 MB 伪随机文本行 + 周期性 `ESC[2J ESC[H` 全屏刷新（模拟 htop 类应用）
- 后台标签最坏路径（`set_visible(false)`，无前端消费）

## 复现

```bash
cargo bench -p nexterm --bench terminal_throughput
```

样例输出：

```text
terminal-engine throughput: 100 MB in 2.304s => 43.4 MB/s
screen after feed: 120x40, lines_len=40
scrollback retained: 33554432 bytes
gbk transcode throughput: 39.0 MB/s
```

## 备注

- 该基准覆盖 M0-T4 的内存验收（喂 100 MB、内存有界）与 §3.2 的吞吐预算（≥10 MB/s 不掉行）。
- 前端渲染路径（Channel→xterm WebGL）另计；背压（§4.4）保证消费端再慢也不放大内存。
- 10 会话并行 RSS ≤ 600MB 的 A3 验收需要 SSH 靶机，列入 nightly e2e（`scripts/` 靶机脚本待部署后执行）。
