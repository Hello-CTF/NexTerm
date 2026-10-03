# 懒猫微服交付验收记录（Go 服务端）

本目录两张 PNG（`01-lazycat-forward-disabled.png`、`02-other-server-listens-any.png`）
是**历史验收证据**：懒猫平台上端口转发置灰/拒绝、自建服务端监听 `0.0.0.0` 的界面实录。
按「历史验收证据不删除」的要求原样保留；它们记录的行为在 Go 版继续成立
（`internal/forward` 的 `Policy{Platform:"lazycat"}` 失败关闭，见下表）。

## Go 化后复验状态（2026-10-03）

| 项 | 状态 | 证据 |
| --- | --- | --- |
| manifest/inject 规则 | ✅ 自动校验通过 | `python3 scripts/verify-manifest-injects.py`：dev/release 两套 `#@build` 裁剪、短服务名路由、两条 inject、`bridgePrefix == fileBridgeRoot`、包内路径一致性全过 |
| 服务端二进制（linux/amd64） | ✅ 本机交叉编译通过 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/nexterm-server` → 静态 ELF x86-64（`file` 实测），`go version -m` 含 `CGO_ENABLED=0` |
| 健康检查 | ✅ 合成数据实测 | `GET /healthz` → `{"ok":true,"service":"nexterm-server","syncOnly":false,...}`；`--sync-only` 时 `syncOnly:true` |
| 完整版 vs sync-only 路由差异 | ✅ 合成数据实测 | sync-only 下 `POST /rpc` → 404、`GET /` → 404；完整版 `/rpc` 正常 |
| RPC | ✅ 合成数据实测 | `POST /rpc {"cmd":"app_info"}` → `{"ok":true,"data":{"name":"NexTerm",...}}` |
| 非回环监听风险告警 | ✅ 合成数据实测 | `--listen 0.0.0.0:*` 启动即向 stderr 打 WARN（slog） |
| 数据目录持久化约定 | ✅ 代码核对 | `internal/platform`：显式 `--data-dir`/`NEXTERM_DATA_DIR` 优先；容器内 `/lzcapp/var` 存在时默认 `/lzcapp/var/nexterm`；manifest 与 Dockerfile 均显式写 `NEXTERM_DATA_DIR=/lzcapp/var/nexterm` |
| 令牌/bootstrap 行为 | ⚠️ 契约已核对，联调待 M25 | CLI（`token`/`rotate-token`）与 `--master-key`  env 已在 `internal/app` 定义；当前 rwig 基线尚未接入令牌存储与静态文件接线（M25/M27 组合中），实测 `token` 返回「同步令牌存储尚未接入」 |
| 转发风险行为 | ✅ 代码核对 | `internal/forward`：`Platform` 为 `lazycat`（大小写/空白不敏感）时创建直接拒绝，先于会话查询 |
| 图标/商店元数据 | ✅ 核对 | `lazycat/lzc-icon.png`、`lazycat/package.yml`（version 0.2.1 与 `wails.json` 唯一版本源一致）；商店截图 `docs/store/*.png` 原样保留 |

## Evidence gaps（真机验收，未做 ≠ 通过）

| 项 | 缺什么 |
| --- | --- |
| 真机 LazyCat 全链路 | 本机无 `lzc-cli` 与微服盒子：`lzc-cli project lint/build`、LPK 实包体积对照 Rust LPK 锚点（~16 MiB 基线）、真机安装/升级/重启、平台路由与 inject 实测，全部未跑 |
| 真机 Pages 验收 | 未对实际部署的 Pages URL 跑响应式与 WS 验收（本地 Vite 构建不算部署证据） |
| WS / blob / 多端 | `/ws/events`、`/ws/channel/{id}`、`/files/blob` 由 M25 `internal/server` 提供，当前 rwig 基线未合并；待 M25/M27 合并并 late-rebase 后补做重连/回放/多客户端实测 |
| 139 条命令全量 | 当前基线 `core.New` 未挂业务模块（`/healthz` 报 commands:2）；全量组合随 M27 落地后复核 |
| 静态站点接线 | `NEXTERM_WEB_ROOT` 的静态 handler 接线随 M25 落地（当前基线 `/` 返回 404）；LPK content 布局与 env 已按契约就位 |

以上缺口与 `.github/acceptance/real-target-gaps.json` 的 `lazycat-box-and-store`、
`deployed-pages` 两条一致；由真机/合并后验收补齐，不以本地结果冒充。
