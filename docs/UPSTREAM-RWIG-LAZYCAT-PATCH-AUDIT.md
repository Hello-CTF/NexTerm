# 纯上游 `rwig` 直接推到懒猫微服，能不能跑？——逐项审计

> **问题**：把上游 Go 版（`rwig` @ `b4df788`，v0.2.2-rc.2）原样拉下来、走完 `lazycat/` 那套流程推到懒猫微服，
> 不做任何本地修改，能正常运行吗？
>
> **答案：不能。最少必须改一处代码，而且这一处没法用配置绕过。**
>
> 审计日期 2026-10-05 ｜ 被测 `origin/rwig` @ `b4df788`（v0.2.2-rc.2）｜ 部署形态：懒猫微服 LPK v2
> 对照物：我方 `local/lazycat-go-dev` @ `2683d16`（= 上游 + 2 条提交，共 9 文件 / +335 −16）

---

## 一、结论总表

| # | 项 | 上游现状 | 纯上游推到懒猫的后果 | 必需性 |
|---|---|---|---|---|
| ① | **Origin 守卫** | 只认 `r.Host`，无转发头支持 | **白屏**（入口 JS/CSS 403）＋ `/rpc`、`/ws` 全 403，**页面完全不可用** | 🔴 **阻断**，必须改代码 |
| ② | **镜像缺 tmux** | Dockerfile 未安装 | 终端**静默降级**为易失会话（照常能敲能看，但关掉网页就没了） | 🟠 建议修（不修不报错，承诺落空） |
| ③ | **migration checksum** | rc.2 就地改了已发布的迁移文件 | **全新部署无影响**；但「旧库 + 新二进制」⇒ 拒绝启动 | 🟡 仅升级场景触发 |
| ④ | 包 ID / 子域 | dev = `cloud.lazycat.app.nexterm.dev` + 子域 `nexterm-dev` | 不影响单线部署 | ⚪ 我方特有，上游不必管 |

**一句话**：上游代码在懒猫上**跑不起来**，卡在第 ①。第 ② 是「能跑但功能缩水且不告诉你」。第 ③ 是存量用户的升级地雷。

---

## 二、① 阻断项：Origin 守卫（必须改代码）

### 上游代码长什么样

`internal/server/transport_security.go`（**rc.1 → rc.2 逐字节未变**，`git diff ce84b62..b4df788` 里没有这个文件）：

```go
var defaultAllowedOrigins = []string{"localhost:*", "127.0.0.1:*"}
...
if strings.EqualFold(parsed.Host, r.Host) {   // ← 唯一的「同源豁免」，只比对 r.Host
    return true
}
for _, pattern := range patterns { ... }      // ← patterns 永远是默认的两个 localhost
```

### 三条事实叠加 ⇒ 必然 403

**事实 1：懒猫网关把 `Host` 改写成上游服务名。** 实测证据（服务端 WARN 日志，`host_candidates` 第一项就是 `r.Host`）：

```
level=WARN msg="rejected request origin" origin=https://evil.example
  host_candidates=nexterm-server:8080,nextermgo.lazycore.heiyu.space
  path=/assets/index-CALkHFvn.js
```

第二条 `nextermgo.lazycore.heiyu.space` 来自网关下发的 `X-Forwarded-Host`；**第一条 `nexterm-server:8080` 就是 `r.Host`**，与 manifest 里 `routes: - /=http://nexterm-server:8080/` 的上游地址一致。

**事实 2：浏览器一定会带 `Origin`。** Vite 产物给入口标签加了 `crossorigin`：

```html
<script type="module" crossorigin src="/assets/index-xxx.js"></script>
<link rel="stylesheet" crossorigin href="/assets/index-xxx.css">
```

于是静态资源走 CORS 模式 ⇒ 必带 `Origin: https://<公网域>`。同源 `fetch` 的 `POST /rpc` 同样带 `Origin`。WebSocket 也带（`ws.go` 把同一份 `AllowedOrigins` 喂给 `OriginPatterns`）。

**事实 3：部署方没有任何手段自救。** 全仓搜索：

```bash
git grep -n "allowed-origin\|ALLOWED_ORIGIN" origin/rwig   # → 空
git grep -n "AllowedOrigins" origin/rwig
# 只出现在：config.go:32（字段定义）、server.go:146/148/150（默认值填充）、
#           transport_security.go:13/19、ws.go:67/115
```

⇒ **`AllowedOrigins` 既没有 CLI flag、也没有环境变量入口，`main.go` 里根本没接线。**
所以「在 manifest 里加个 `NEXTERM_ALLOWED_ORIGINS`」是**无效的 —— 上游代码不读它**。

### ✅ 已用**纯上游二进制**实测复现（2026-10-05）

不是推理。把 `origin/rwig` @ `b4df788` 单独 checkout 到干净工作区
（已确认 `grep -c requestHostCandidates internal/server/transport_security.go` = **0**，即不含我方任何修复），
`go build ./cmd/nexterm-server` 出真实二进制（Mach-O arm64，55 MB）后本机对照：

```
被测二进制: 纯上游 rc.2（b4df788）

=== 同一端口、同一路径，只改 Host / Origin 两个头 ===
  Origin=公网域  Host=nexterm-server:8080  <反代实况> -> 403
  Origin=公网域  Host=公网域         <无改写对照> -> 200
  不带 Origin  Host=nexterm-server:8080                    -> 200
  Origin=同源    Host=127.0.0.1:18080                      -> 200

=== 反代实况下 403 的响应体（浏览器看到的就是这个）===
origin is not allowed

=== 同一条件打 /rpc（同源 fetch POST 也会带 Origin）===
  POST /rpc 带公网 Origin + 改写 Host                   -> 403
  POST /rpc 不带 Origin（对照）                        -> 200
```

**读法（每一行都是一个判据）：**

| 对照 | 说明 |
|---|---|
| 第 1 行 vs 第 2 行 | 只改 `Host` 就决定 403/200 ⇒ 服务端**只认 `Host` 头** |
| 第 1 行 vs 第 3 行 | 去掉 `Origin` 立刻 200 ⇒ 唯一变量确实是 `Origin` |
| 第 4 行 | `Origin` 与 `Host` 一致时放行 ⇒ 印证「同源豁免」的判定依据 |
| `/rpc` 那两行 | **不止静态资源** —— 同源 `fetch` 的 POST 也带 `Origin`，功能同样是死的 |

⇒ 配合「懒猫网关把 `Host` 改写成 `nexterm-server:8080`」这一平台事实（见上），
**纯上游推到懒猫 = 必然白屏**，`#root` 永远是空的。这不是「某个页面打不开」，是**整个应用没起来**。

### 强烈建议的修法（按推荐度）

| 方案 | 做法 | 评价 |
|---|---|---|
| **A. 同源判定兼收转发头** | `requestOriginAllowed` 除 `r.Host` 外，再加 `X-Forwarded-Host` 与 RFC 7239 `Forwarded` 里的 host | ✅ **推荐**。零配置、换域名/换机器自动跟着走。懒猫**已实测会下发** `X-Forwarded-Host`，所以这一条单独就够 |
| **B. 静态资源不参与 Origin 校验** | 把 `/assets/*` 这类无副作用的 GET 排除在 guard 之外 | ⚠️ 单独只能救回白屏，`/rpc` `/ws` 仍是 403 ⇒ 功能还是死的 |
| **C. 给 `AllowedOrigins` 开配置入口** | 加 `--allowed-origin` / `NEXTERM_ALLOWED_ORIGINS` 并接线 | ⚠️ 单独不够（静态资源也走 guard），且要求部署方知道自己的域名 |

**最小可用组合是 B+C；最干净的是 A（实测可行）。** 我方落地用的是 **A + C 双保险**（C 用部署期渲染的 `{{ .S.AppDomain }}`，不写死域名）。

> 完整可粘贴的 issue 正文（含两条 curl 最小复现、逐行根因、CI 为何抓不到）见
> **`UPSTREAM-RWIG-ISSUE-origin-guard.md`**。

---

## 三、② 降级项：镜像没装 tmux（建议修，因为不报错）

上游 `lazycat/image/Dockerfile` 的安装列表**只有**：

```
ca-certificates  openssh-client  sshfs  fuse3  tzdata
```

**没有 tmux。** 而 rc.2 把「缺 tmux」的行为从**报错**改成了**静默降级** —— 测试名把这件事说得很清楚：

| 版本 | 测试名 | 含义 |
|---|---|---|
| rc.1 | `TestProductionDurableMissingTmuxIsUnavailableWithoutFallback` | 缺 tmux ⇒ durable **明确不可用，无回退** |
| rc.2 | `TestProductionLocalAttachFallsBackToVolatileTabWithoutTmux` | 缺 tmux ⇒ **回退到易失 tab（volatile）** |

**后果**：终端照常能敲能看、`terminal_attach` 照常返回 tab id，**一条错误日志都不会有**；
但会话不再由服务端托管 —— 而 manifest 的 `usage` 文案向用户承诺的是：

> 「关掉网页不会结束会话，换一台设备打开即可接管原来那条终端」

这句话在缺 tmux 时**直接落空**，用户不会收到任何提示。
rc.1 时代反而更容易发现（会直接抛 `unsupported: durable terminal unavailable`）。

我方处置：Dockerfile 补 `tmux`（一行）。

---

## 四、③ 升级陷阱：migration checksum（存量用户会炸）

rc.2 做了一次**仓库级去注释清理**，误伤了**已经发布过**的迁移文件 `migrations/0001|0004|0005.sql`
（只删 `--` 注释，语义零变化）。但 `internal/store/migrate.go` 把

```go
checksum := sha256.Sum256(contents)   // 迁移文件的原始字节
```

当作**契约**写进 `schema_migrations.checksum`，字节一变就拒绝启动：

```
数据库迁移错误: migration 1 (init) does not match its applied checksum
```

- **全新部署**（空库）⇒ 无影响，checksum 当场写入必然自洽。
- **「旧库 + 新二进制」**（= 商店里所有已装 rc.1 的用户升级 rc.2）⇒ **起不来**。

**为什么 CI 抓不到**：所有测试都用 `t.TempDir()` 建**空库**，checksum 当场写入必然匹配。

⇒ **已在懒猫应用中心装过 rc.1 的用户，升级 rc.2 会直接 `Status_Error`。**

> 完整分析与三种修法见 **`UPSTREAM-RWIG-ISSUE-migration-checksum.md`**。

---

## 五、④ 我方特有（上游不用做）

这两处是**我方为了「Rust 线与 Go 线在同一台微服上并排跑」**才加的，跟「能不能跑」无关：

| 改动 | 我方值 | 上游值 | 为什么我方要改 |
|---|---|---|---|
| dev 包 ID | `cloud.lazycat.app.nextermgo.dev` | `cloud.lazycat.app.nexterm.dev` | 与另一条产品线的 dev 包区分 |
| dev 子域 | `nextermgo` | `nexterm-dev` | 两条线各自要有独立入口，否则先装的占住域名 |

`lzc-manifest.yml` 里的 `subdomain` 是 `#@build if profile=dev` 分支，`project deploy` 读 dev 配置、`project release` 读主配置。

---

## 六、顺带发现：上游缺「开发者向」的懒猫部署说明

仓库里的 md 只有 `README.md` / `AGENTS.md` / `.github/release-template.md` / `lazycat/injects/README.md`。
README 的「懒猫微服」一节只讲了**用户怎么装**（应用中心安装），**没有开发者向的构建/部署流程**。

照配置能推断出来（`lzc-build.yml` 的 `buildscript: ./image/build-server.sh` + `lzc-cli project deploy`），
但「dev 用哪份配置、子域怎么隔离、包 ID 怎么改」全靠自己摸。建议补一节 `lazycat/README.md`。

---

## 七、给上游的最小改动清单

按「改完就能在懒猫上跑」排序：

1. **（必须）** `internal/server/transport_security.go`：`requestOriginAllowed` 兼收 `X-Forwarded-Host` / `Forwarded`，
   或把 `/assets/*` 等静态资源排除出 `transportGuard`。**不改这一条，懒猫上永远是白屏。**
2. **（建议）** `internal/server/config.go` + `cmd/nexterm-server/main.go`：给 `AllowedOrigins` 开
   `--allowed-origin` / `NEXTERM_ALLOWED_ORIGINS` 入口并接线（现在这个字段**永远拿不到值**）。
3. **（建议）** `lazycat/image/Dockerfile`：安装列表补 `tmux \`。否则「关掉网页不断会话」是空头承诺，
   而且**不报错**。
4. **（必须，针对存量用户）** 处理 `migrations/0001|0004|0005.sql` 的 checksum 变更：要么**恢复原始字节**，
   要么给迁移文件加一条「已发布文件禁止改动」的 CI 校验。
5. （可选）`lazycat/README.md`：补开发者向的构建部署说明。

---

## 八、如何自己验一遍（每项都能独立复现）

```bash
# ① Origin 守卫 —— 不需要懒猫，本机两条 curl 就能看到
nexterm-server --listen 127.0.0.1:8080 --web-root ./web
curl -i -H 'Origin: https://app.example.com' -H 'Host: nexterm-server:8080' \
     http://127.0.0.1:8080/assets/anything.js          # → 403 origin is not allowed
curl -o /dev/null -w '%{http_code}\n' -H 'Host: nexterm-server:8080' \
     http://127.0.0.1:8080/assets/anything.js          # → 200（唯一变量就是 Origin）

# ③ migration checksum —— 用新旧两个二进制打同一个库
<rc.1 二进制> --data-dir /tmp/t --listen 127.0.0.1:8080 &   # 建库
<rc.2 二进制> --data-dir /tmp/t --listen 127.0.0.1:8080     # → migration 1 does not match its applied checksum

# ② tmux —— 看 Dockerfile 的 apt 列表，或直接
docker run --rm <镜像> sh -c 'command -v tmux || echo "tmux 不存在"'
```

---

## 附：本次判定所依据的核对点

| 核对点 | 方法 | 结果 |
|---|---|---|
| Origin 守卫 rc.1→rc.2 有无变化 | `git diff ce84b62..b4df788 --stat -- internal/server/transport_security.go` | **未出现在 diff 中 ⇒ 逐字节未变** |
| `AllowedOrigins` 有无配置入口 | `git grep "allowed-origin\|ALLOWED_ORIGIN" origin/rwig` | **空 ⇒ 无入口** |
| 网关是否改写 Host | 服务端 WARN 日志的 `host_candidates` | **第一项 = `nexterm-server:8080`** |
| 网关是否下发 `X-Forwarded-Host` | 同上，第二项 | **下发** |
| rc.2 鉴权是否上游自洽 | 上游 `lzc-manifest.yml` 自带 `public_path` + `injects/gateway-auth` + `NEXTERM_GATEWAY_AUTH` | **自洽，无需我方改动** ✅ |
| tmux 行为变更 | rc.1 `...UnavailableWithoutFallback` vs rc.2 `...FallsBackToVolatileTabWithoutTmux` | **rc.2 改为静默降级** |
| 我方改动总量 | `git diff origin/rwig..local/lazycat-go-dev --stat` | 9 文件 / +335 −16 |
| 缺陷可用**纯上游二进制**复现 | 干净 worktree checkout `b4df788` + `go build` + 单变量 curl 对照 | **反代实况 403 / 无改写 200** |
