# 懒猫微服交付验收记录（Go 服务端）

本目录两张 PNG（`01-lazycat-forward-disabled.png`、`02-other-server-listens-any.png`）
是**历史验收证据**：懒猫平台上端口转发置灰/拒绝、自建服务端监听 `0.0.0.0` 的界面实录。
按「历史验收证据不删除」的要求原样保留；它们记录的行为在 Go 版继续成立
（`internal/forward` 的 `Policy{Platform:"lazycat"}` 失败关闭，见下表）。

## Go 化后复验状态（2026-10-03，rwig@6f8847e + M12 交付链）

以下每条都是本日在 `feat/rwig-release-lazycat-r12`（4e3d491）上重跑的，
不是从旧基线抄来的结论：

| 项 | 状态 | 证据 |
| --- | --- | --- |
| manifest/inject 规则 | ✅ 自动校验通过 | `python3 scripts/verify-manifest-injects.py`：dev/release 两套 `#@build` 裁剪、短服务名路由、两条 inject、`bridgePrefix == fileBridgeRoot`、包内路径一致性全过 |
| LazyCat 构建链（Go 共享入口） | ✅ 全链通过 | `bash lazycat/image/build-server.sh`：前端构建（tree sha256 `4852a371…`）→ `lazycat/content/web` + `lazycat-injects` 就位 → `node scripts/build.mjs server --release --os=linux --arch=amd64` → 静态 ELF x86-64、CGO_ENABLED=0，4/4 自检通过。无 cargo-container、无 qemu、无 pick-target.sh |
| 服务端二进制（linux/amd64） | ✅ 本机交叉编译通过 | 42,115,234 B，sha256 `353b9a6c…`；`file` 实测静态 ELF x86-64，`go version -m` 含 `CGO_ENABLED=0`（报告 `target/go-build/nexterm-server-linux-amd64.artifact.json`） |
| 健康检查 | ✅ 合成数据实测 | 完整版 `GET /healthz` → `commands:140, syncOnly:false, version:0.2.1`，vault/retention 状态在位；`--sync-only` → `commands:3, syncOnly:true, webRoot:null` |
| 完整版 vs sync-only 路由差异 | ✅ 合成数据实测 | sync-only 下 `POST /rpc` → 404、`GET /` → 404、`POST /sync/rpc` 无令牌 → 403「同步令牌无效或缺失」；完整版三者行为相反 |
| RPC | ✅ 合成数据实测 | `POST /rpc {"cmd":"app_info"}` → `{"ok":true,"data":{"name":"NexTerm",…}}`；未知命令 → `not_found` 错误信封 |
| 静态站点接线 | ✅ 合成数据实测 | 完整版 `GET /` 返回 `index.html`（注入 `window.__NEXTERM_TRANSPORT__="web"` 标记）；SPA marker/缓存/HEAD/路径穿越拒绝由 `internal/server/static_test.go` 覆盖 |
| 令牌/bootstrap 行为 | ✅ 实测 | `nexterm-server token --data-dir …` 与 `rotate-token` 均返回真实令牌（令牌存储与静态接线已随 M25 组合落地，不再是「尚未接入」） |
| WS 回放 / 重连 / 多客户端 | ✅ go test 实测 | `go test ./internal/server`：`TestWebSocketReplayLiveBoundaryPreservesArrivalOrder`（回放→直播边界保序）、`TestWebSocketChannelPendingReplayAndFrameTypes`、`TestWebSocketEventsEnvelopeAndUnsubscribe`、hub pending replay 集成测试全过 |
| blob 暂存 | ✅ go test 实测 | `TestBlobStageReserveStreamAndDelete`、`TestBlobPersistenceTTLAndIDBoundary`：stage/reserve/stream/delete 与 TTL 持久化全过 |
| 双进程同步 E2E | ✅ 实测 22/22 | `python3 scripts/e2e-sync-local.py`：两个真实服务端之间令牌网关、实例隔离、凭据重封导出导入、幂等、墓碑反传、sync-only 重启后攻击面收敛（22 项断言全过，报告 `target/e2e-sync/`） |
| 全量 Go 测试 | ✅ 实测 | `go test ./internal/...` 全部通过（含 `./internal/app`、`./internal/sync`） |
| 非回环监听风险告警 | ✅ 合成数据实测 | `--listen 0.0.0.0:*` 启动即向 stderr 打 WARN（slog） |
| 数据目录持久化约定 | ✅ 代码核对 | `internal/platform`：显式 `--data-dir`/`NEXTERM_DATA_DIR` 优先；容器内 `/lzcapp/var` 存在时默认 `/lzcapp/var/nexterm`；manifest 与 Dockerfile 均显式写 `NEXTERM_DATA_DIR=/lzcapp/var/nexterm` |
| 转发风险行为 | ✅ 代码核对 | `internal/forward`：`Platform` 为 `lazycat`（大小写/空白不敏感）时创建直接拒绝，先于会话查询 |
| 图标/商店元数据 | ✅ 核对 | `lazycat/lzc-icon.png`、`lazycat/package.yml`（version 0.2.1 与 `wails.json` 唯一版本源一致）；商店截图 `docs/store/*.png` 原样保留 |
| **体积门（Rust 同形态对照）** | ❌ **FAILED，不得写成通过** | 共享入口产物对官方 v0.2.1 同形态参照**全面超标**：linux/amd64 server 42,115,234 B vs 17,360,408 B（2.43×）；darwin/arm64 server 37,711,218 B vs 同机基线 17,172,784 B（2.20×）。pre-Eino custom-Go 基线 34,844,834 B ⇒ full-Eino 增量 +7,270,400 B；darwin server 无 pre-Eino 同形态测量（evidence-gap）。详见 `docs/acceptance-rwig/baseline/README.md` |

## 命令面与 runtime coverage（如实，未完成 ≠ 通过）

- 组合服务端命令面：`/healthz` 实测 **完整版 140 条 / sync-only 3 条**
  （140 = 138 条 Rust 清单重叠 + `ai_answer` + `terminal_resize_flush`；
  `credential_save` 为 Rust-only，Go 侧未实现）。
- 前端 parity 清单：141 methods / 140 unique，`frontend_only = [ai_answer, terminal_resize_flush]`
  （M20 时为 140/139、`frontend_only=[ai_answer]`，终端网格加入 `terminal_resize_flush`）。
- **runtime coverage 仍是 2/139 verified**（`app_info`、`app_platform`），
  其余为 implemented-unverified / not-implemented，`feature_complete=false` ——
  见 `docs/acceptance-rwig/baseline/go-coverage.json`，不写成完成。

## Evidence gaps（真机验收，未做 ≠ 通过）

| 项 | 缺什么 |
| --- | --- |
| 真机 LazyCat 全链路（含 LPK 安装/升级） | 本机无 `lzc-cli` 与微服盒子：`lzc-cli project lint/build`、LPK 实包体积对照 Rust LPK 锚点（v0.1.3 ≈ 15.97 MiB / dev-v0.2.0 = 18,718,208 B，均为仓库记录值）、真机安装/升级/重启、平台路由与 inject 实测，全部未跑 |
| 真机 Pages 验收 | 未对实际部署的 Pages URL 跑响应式与 WS 验收（本地 Vite 构建不算部署证据） |

以上缺口与 `.github/acceptance/real-target-gaps.json` 的 `lazycat-box-and-store`、
`deployed-pages` 两条一致；由真机/部署后验收补齐，不以本地结果冒充。
WS / blob / 多端 / 静态 / 令牌这些「旧基线未合并」造成的缺口已随 M25 组合落地
在本机验证关闭，不再列为缺口；真机行为仍以盒子实测为准。

## 发布产物口径（M29 合同 6b17373）

Release 产物固定为八个包：Windows `NexTerm_<v>_x64-setup.exe` / `_arm64-setup.exe`，
macOS `NexTerm_<v>_x86_64.dmg` / `_aarch64.dmg`，Linux 桌面
`NexTerm-desktop_<v>_linux_amd64.tar.gz` / `_arm64.tar.gz`，Linux 服务端
`NexTerm-server_<v>_linux_amd64.tar.gz` / `_arm64.tar.gz`。
`NexTerm-onlyServer-*` 与 `sync-archive` 不再是合法产物：sync-only 是同一个
服务端包内的运行时选项（包内含 `nexterm-server.service` 与
`nexterm-onlyserver.service` 两个单元与两份 env 示例）。website/deploy 文案
已按此口径对齐；LazyCat LPK 仍直接消费静态服务端二进制，不经 tar 归档。
