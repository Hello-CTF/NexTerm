# 上游 `rwig` 分支评审：Go/Wails 重写版 vs 我们的 Rust/Tauri 版

- **评审时间**：2026-10-04
- **上游**：`ProbiusOfficial/NexTerm` → `origin/rwig` @ `ce84b628`（tag `v0.2.2-rc.1`）
- **我们的基线**：`master` @ `25c2fa0`（tag `v0.2.1`）
- **分叉点**：`25c2fa0` —— 即 `rwig` 从我们当前 master 的 HEAD 分出，`rwig..master` 贡献数为 **0**，我们这边一条都没落下
- **规模**：320 个提交，1069 个文件变更，+145,512 / −31,602
- **评审机**：Apple M4 / macOS 26.6.2 (Darwin 25.6.0) / arm64
- **工作副本**：`/Users/macmini/nexterm-wt/rwig`（独立 worktree，不污染主工作区）

---

## 0. 一句话结论

**这不是「等价移植」，是「在保留全部功能面的前提下做的架构改良 + 大幅功能扩展」。**
技术底子很硬：模块边界是真的、机械验证是真的、自评数字是诚实的（甚至偏保守）。
未压缩时，代价是**体积翻 2.2 倍**、**内存翻 2.1 倍**；换来的是**编译快 6 倍**、**启动快 3.2 倍**、**依赖树小一个数量级**。

⚠️ **体积这一条有前提，而且 §4.5 一度把它错误反转**：§4.5 得出过「Go+UPX 比 Rust 小 34%」—— 那是**不对称比较**（拿 Go 的 UPX 版去比 Rust 的**未压**版，被比的那个还是 macOS 二进制，而 UPX 本来就拒绝它）。同口径重做后（双方都 `upx --lzma`），**Rust 在 Linux 上仍小 1.9×、在 macOS 上小约 4×**。完整修正 +「到底还能压多少」见 **§4.6**。

⛔ **另有一条阻断级实测（2026-10-04，本轮新增）**：把它部署到懒猫微服（反代）后**页面白屏，而且不止白屏 —— `/rpc` 与 `/ws/*` 也全 403**。根因是 `transportGuard` 的 Origin 校验在 Host 被反代改写时把自己也挡了，且这个缺陷**已在 `origin/rwig` 上 ⇒ release v0.2.2-rc.1 同样含此问题**。详见 **§5.2 ⑤**。上游 CI 与本地开发全在 `localhost` 上跑，所以测不出来。

✅ **同日已在本仓修复并实机验收**（`local/lazycat-go-dev` 的 `ca7a713`）：改动只有 8 个文件，根因那处是让同源判定兼收 `X-Forwarded-Host`；顺带补上镜像缺的 `tmux`（缺它整个终端功能是死的，见 §5.4）。验收结果、以及「懒猫网关到底发不发那个头」这个原未知量的实测答案，见 **§5.3 / §5.4**。

🔄 **rc.2 追更（2026-10-05）**：上游已推进到 `b4df788`（tag `v0.2.2-rc.2`，+300 提交）。我方两条修复**都仍必要** —— 上游**没碰** `transport_security.go`（`requestOriginAllowed` 仍只比对 `Host`），Dockerfile **仍未装 tmux**（但缺 tmux 的行为变了：rc.2 起改为**静默降级**为易失 tab，不再是显式报错 —— 见 §5.4 修正）。

⛔ **新增一条比 Origin 更严重的阻断缺陷**：rc.2 做了一轮仓库级「去注释」清理，**误改了已发布的 `migrations/0001_init.sql` / `0004_outcome.sql` / `0005_cron.sql`**（只删了 SQL 注释，语义零变化）。但 `internal/store/migrate.go` 拿**文件字节的 sha256** 当不可变契约 ⇒ **任何存量数据库升级 rc.2 都拒绝启动**（`migration 1 (init) does not match its applied checksum`），与部署形态无关，商店里已安装的用户一并中招。本地已用「rc.1 建库 → rc.2 打开」逐字复现，并验证「修 checksum 后数据可无损救回」。完整分析、最小复现与修法建议见 **`UPSTREAM-RWIG-ISSUE-migration-checksum.md`**。

---

## 1. 上游是什么

`rwig` 把后端从 Rust/Tauri 整体重写为 **Go 1.26 + Wails v3（alpha.98）**：

| | 我们（master） | rwig |
|---|---|---|
| 桌面壳 | Tauri v2（Rust） | Wails v3 alpha.98 |
| 服务端 | axum（Rust，`not(feature="desktop")`） | 标准库 net/http（Go） |
| 双态实现方式 | 一份源码 + cargo feature + `ipc_shim` 门面 | 一份 module 组合，桌面/服务端共用 `internal/app/production` |
| 前端 | React 19 + Vite（共用产物） | 同一份 React 前端，**大幅扩展** |

关键背书：`src-tauri/` 目录在 `rwig` 树里**已整个删除**，`go.mod` 取代了 `Cargo.toml`。

---

## 2. 规模对比

| 指标 | 我们（Rust/Tauri） | rwig（Go/Wails） | 倍数 |
|---|---:|---:|---:|
| 后端文件数 | 75 (`*.rs`) | 676 (`*.go`) | 9.0× |
| 后端生产代码 | 27,421 行（含内联测试） | 53,656 行 | ~2.0× |
| 后端测试代码 | 含在上一行（`#[cfg(test)]`） | 56,105 行 | — |
| 前端文件数 | 57 | 152 | 2.7× |
| 前端代码 | 23,523 行 | 43,312 行 | 1.8× |
| 前端测试 | 有（Vitest） | 11,728 行 / 53 文件 | — |
| IPC 命令数（`/healthz` 实测） | **139** | **155** | +16 |
| 直接依赖 | 68（`Cargo.toml`） | 23（`go.mod`） | 1/3 |
| 传递依赖 | **740**（`Cargo.lock`） | 117（23 直接 + 94 间接） | 1/6 |
| CI 构建矩阵 | 2（win/mac 质量门） | 8（3 平台 × desktop/server） | 4× |

`internal/` 的 24 个包与我们的 Rust 模块几乎一一对应（`ai` / `session` / `terminal` / `vault` / `sync` / `docker` / `mount` / `forward` / `tasks` / `outcome` / `durable` …），说明**功能面是照着我们的清单逐条对齐**的（`testdata/parity/inventory.json` 里 `source` 字段直接写着 `src-tauri/src/commands/mod.rs`）。

AI 模块占 Go 后端的 35%（18,868 行生产 + 19,518 行测试）—— 这是扩展最多的地方（引入 `cloudwego/eino` 做工具编排）。

---

## 3. 架构对比：Go 版针对性解决我们的痛点

### 3.1 「两处同步补」的结构性消除

我们的痛点（记忆铁律 #2）：**新用 Tauri 顶层项或 trait 方法必须两处同步补**（桌面白名单 + `server_impl`），漏一处则两条路径同时报错。

Go 版的解法是**结构性消除**，不是再写一遍：

> `ARCHITECTURE.md`：*"Desktop and server must receive the **same module composition**, with capability-specific behavior decided **inside shared services** rather than duplicated handlers."*

实测验证了这条声称 —— `internal/ipc`（wire DTO / dispatcher）与 `internal/platform` 对 `internal/*` 业务包**零引用**：

```
$ grep -rhoE '"github.com/.../internal/[a-z]+"' internal/ipc/*.go | sort -u
(空)
```

`internal/app` 是依赖极轻的应用核心；`internal/app/production` 是桌面/服务端共用的具体领域图；两者都不 import Wails。这是**正确的分层**，比我们的 `ipc_shim` 门面更彻底。

### 3.2 命令面平齐做成了机械验证

朋友把我们「手写 curl 打 `/rpc` 要判断参数名是不是 `args`」这种易错点，做成了**显式 API + 自动对账**：

- `ipc.Register[In,Out](d, name, handler)` —— 平铺参数
- `ipc.RegisterNested[In,Out](d, name, handler)` —— 参数名本身叫 `args` 的那层嵌套，由框架拆
- `testdata/parity/inventory.json` + `scripts/parity/inventory.py --check` —— 拿**冻结的 Rust 命令清单**（`rust-registry.json`，139 条，记录来源 commit `25c2fa0`）与**实时前端 facade** 做减法对账，差额必须逐条解释（`reconciliation.rust_only` / `frontend_only`）
- `tests/parity/go/cmd/surfaceprobe` —— 一个 Go 程序，把 Go 版真实注册的命令面 dump 成 JSON，用来和 inventory 比

我实测跑了一遍 `inventory.py --check`：**exit 0，通过**。

### 3.3 panic 兜底

`internal/ipc/dispatcher.go` 的 `Dispatch` 带 `defer recover()`，任何 handler panic 都会被转成 `{ok:false,error}` 响应而不是崩进程。

对比我们的对应教训（记忆：`panic = "unwind"` 那条注释）—— 我们在 2026-09-28 因为 `abort` 丢过一次真实会话，才改回 `unwind`。Go 版用 runtime 级 recover 天然覆盖了这个类别。

---

## 4. 性能实测（同机、串行、可复现）

全部数据在**同一台机器**上由本次评审跑出，命令见附录。朋友的 `docs/acceptance-rwig/baseline/` 里另有一份他自己机器上的基线（含 Rust 侧原始日志），可交叉参照。

| 指标 | 我们（Rust/Tauri） | rwig（Go/Wails） | 结论 |
|---|---:|---:|---|
| **server 冷构建**（清空缓存） | **119.0 s** | **19.8 s** | Go 快 **6.0×** |
| server 热构建（无改动） | 0.81 s | 0.48 s | Go 快 1.7× |
| **server 体积** darwin/arm64 | **17.1 MB** | **36.9 MB** | Rust 小 **2.16×** |
| server 体积 linux/amd64 | 16.3 MB（历史锚点） | 38.6 MB | Rust 小 2.36× |
| **启动 → `/healthz` 可用** | **370 ms** | **114 ms** | Go 快 **3.2×** |
| **空载物理内存**（vmmap footprint） | **7.3 MB** | **15.3 MB** | Rust 省 **2.1×** |
| 单测耗时（server 模式） | ~50 s / **301 passed / 0 failed** | 52.2 s / 44 包，**1 failed** | 相当 |
| 终端引擎吞吐（UTF-8 feed） | 80.8 MB/s（120×40 + 清屏序列） | 69.7 MB/s（80×24 纯文本行） | 同量级 † |

† 两侧基准口径不同（屏幕尺寸、负载构造不同），**只能当同量级看，不能当精确倍数**。

补充事实：

- **体积**：Go 侧已用 `-trimpath -ldflags "-s -w"` 优化过（相对未优化省 ~30%，见其 `go-size-optimization-r37.md`）。即便如此仍是我们 2.16×。差距来源是 Go runtime + 反射/JSON 元数据 + `modernc.org/sqlite`（纯 Go SQLite 实现，无 CGO）—— 换来了 `CGO_ENABLED=0` 的静态单文件。
- **启动**：Go 快 3.2× 有点反直觉（通常 Rust 启动更快）。原因是我们的 server 启动期有 tokio runtime + sqlx migrate + vault 初始化等同步工作；Go 版把初始化铺开得更轻。
- **内存**：Rust 省一半，这是 Rust 无 GC + 无 runtime 的预期收益。
- **构建**：Go 快 6× 是最大的一项优势，对 CI 成本和迭代速度是实打实的。

---

## 4.5 UPX 补充实测：体积结论会反转，但有硬边界

> 起因：评审后有人指出「Go 的产物还能做 UPX 压缩」。这一点**成立，而且比预期更彻底** —— 但适用平台有硬限制。以下是实测。

### 4.5.1 压缩效果（UPX 5.2.1，`-9`）

| 目标 | 原版 | UPX 后 | 压缩率 |
|---|---:|---:|---:|
| linux/amd64 | 38,584,480 B (36.8 MiB) | **13,042,464 B (12.4 MiB)** | 33.8% |
| linux/arm64 | 35,782,816 B (34.1 MiB) | **11,349,228 B (10.8 MiB)** | 31.7% |
| windows/amd64 | 39,432,192 B (37.6 MiB) | **13,335,040 B (12.7 MiB)** | 33.8% |
| darwin/arm64 | 36,879,026 B | **不支持** | — |

**Linux/Windows 上 Go 侧体积大幅下降**：Go + UPX 的 10.8 MiB，相对其自身 36.8 MiB 是 **−71%**。

> ⚠️ **本节下面的对比口径是错的，已在 §4.6.2 修正。** 当时我把「Go 的 UPX 版」和「Rust 的**未压**版」放在一起比 —— 而且被比的那个 Rust 二进制是 macOS 产物，UPX 本来就拒绝它。
> 事实上**我们的 Linux 二进制同样能 UPX**：16.50 MiB → **4.92 MiB**（`--lzma`）。同口径下 Rust 仍小 **1.9×**。这个错误让本节一度得出「体积结论反转」的结论，是错的。

macOS 侧则是硬墙 —— UPX 5.2.1 直接拒绝：

```
upx: nexterm-server-darwin-arm64: CantPackException: macOS is currently not supported (try --force-macos)
```

对 Rust 二进制同样拒绝，所以这不是 Go/Rust 的差别，是平台的差别。

### 4.5.2 关键疑点：会不会破坏 Go 的运行时元数据？

rwig 的 `go-size-optimization-r37.md` 把 UPX 和「pclntab/build-info stripping」并列为 forbidden，理由是 *"the runtime metadata needed for stack symbolization stays intact"* —— 隐含前提是 **UPX 会破坏它**。

我在容器里（linux/arm64 原生）做了两组验证，**这个前提不成立**：

**① SIGQUIT 栈转储对比**（`kill -QUIT`，需要 pclntab 才能解析函数名和文件行号）

| | 原版 | UPX 版 |
|---|---:|---:|
| goroutine 数 | 18 | 18 |
| 符号化行数 | 94 | 94 |
| 内容 | `runtime/os_linux.go:73`、`runtime/proc.go:3278` … | **完全一致** |

**② 可逆性验证**（`upx -d` 还原后比对哈希）

```
原版      267d57144d305e093b97683b685d0dfea8dad8020e46cdefc4abff016560910c
还原产物  267d57144d305e093b97683b685d0dfea8dad8020e46cdefc4abff016560910c   ← 逐字节一致
```

**③ 压缩确定性**（同输入压两次）

```
a66917d78a303d393b253fe0dd67c63376aa6b18a79ec8fbe11f63cb109a3577  （两次相同）
```

结论：UPX 对 Go 二进制是**无损、确定性、保元数据**的。rwig 那条禁令如果建立在「UPX 会破坏栈符号化」上，**理由是错的**；如果建立在 AV 误报和代码签名上，**理由是成立的**（见下）。

### 4.5.3 真实代价（同容器实测）

| 指标 | 原版 | UPX 版 | 变化 |
|---|---:|---:|---|
| 启动到 `/healthz`（5 次采样） | 40 / 60 / 60 / 30 / 40 ms | 250 / 190 / 250 / 240 / 240 ms | **约 5×（+190 ms）** |
| 空载 RSS | 25,876 kB (25.3 MiB) | 34,724 kB (33.9 MiB) | **+34%（+8.6 MiB）** |

自解压的固定开销：每次冷启动要多花约 190 ms，常驻内存多约 8.6 MiB。对常驻服务端无所谓；对**频繁启动的桌面端和 CLI**（`nexterm-server token` 这类）是可感知的。

### 4.5.4 平台适用性

| 平台 | UPX | 障碍 |
|---|---|---|
| Linux 服务端 | ✅ 可用 | 无硬障碍；体积收益最大 |
| Windows 桌面 | ⚠️ 技术上可用 | **杀软误报**：UPX 是恶意软件最常用的打包器（Darktrace 记录的 Redis 挖矿木马 Migo 就是 UPX 打包的 Go 二进制）。学术实测表明「反病毒引擎对加壳存在系统性偏见，倾向于把加壳等同于恶意意图」；有引擎对 UPX 合法软件的判定准确率仅 **0.51**。对一个主打 Windows 的运维终端来说，被标红等同于产品死亡 |
| macOS 桌面 | ❌ 不支持 | UPX 直接拒绝 Mach-O；且 PyInstaller 官方文档明确记录：*"the UPX-compressed files fail the validation check of the codesign utility, and therefore cannot be code-signed（Apple M1 平台的硬性要求）"*。即使 `--force-macos` 绕过，签名/公证链条也断了 |

### 4.5.5 对结论的修正

1. **「Go 比 Rust 大 2.16×」需要加限定**：那是**未压缩**的对比。两侧在 Linux 上都能 UPX，但本节的原始比较**不对称**（Go 压过 vs Rust 没压），已在 §4.6.2 按同口径修正：`upx --lzma` 后 Rust **4.92 MiB** vs Go **9.32 MiB**，**Rust 仍小 1.9×**。
   - 另外，本节只用了 UPX 的默认档（`-9`，NRV 算法）。换 `--lzma` 后 Go 侧可再小 **25%**（12.44 → 9.32 MiB），距理论地板只剩 1.4% —— 见 §4.6.1。
   - 还有一层：我们的 LPK 分发通道**本身是 gzip 的**，而 `upx -9` 的产物大小与 `gzip -9` 原始二进制几乎相等（12.44 vs 12.55 MiB）⇒ 在容器镜像里，**默认档 UPX 等于白压**。只有 `--lzma` 能真正突破通道压缩比（§4.6.2）。
2. **但 UPX 不是免费午餐**：换来 -68% 体积的代价是启动 5×、内存 +34%，并且**在 macOS 上完全不可用**、**在 Windows 上有真实的被杀软误报风险**。
3. **建议**：如果要做，只对 **Linux 服务端**产物启用（体积收益最大、无签名/AV 障碍、常驻场景不在乎启动开销）；桌面端（尤其 Windows 和 macOS）保持不压。这也正好对应工程实践里那条经验规则 —— *「仅对 CLI/服务端工具启用 UPX，GUI 工具禁用」*。
4. **要提醒的是**：UPX 若进入流水线，UPX 本身就成为构建依赖（版本需钉死），且它会使产物的不可解释字节增多 —— 这对一个需要向用户解释「这个二进制里是什么」的开源项目是隐性成本。

---

## 4.6 「还能再压多少」：压缩地板、分发通道与真正的杠杆

> 起因：看完 §4.5 后有人追问「那**还有别的压缩空间**么」。这一节全部是同机实测。
>
> **先给结论**
> 1. **压缩器这条路已经到底了** —— `upx --lzma` 距理论下限（`xz -9`）只剩 **1.4%**，继续调参没有意义。
> 2. **UPX 的默认档（`-9`）在我们的分发通道里几乎白干** —— LPK 的镜像层本来就是 gzip，而 `gzip -9(原始二进制)` ≈ `upx -9`（12.55 vs 12.44 MiB）。只有 **`--lzma`** 能真正压低镜像层。
> 3. **真正的空间在编译器与依赖，不在压缩器**：Rust 换 profile **−46.6%**；Go 禁内联 **−10.4%**；Go 侧最大的单块依赖是 `redis/go-redis`（3.37 MiB），**比整个自研后端还大 1.8 倍**。
> 4. **§4.5 的对比口径作废**（见下 §4.6.2）：同口径下 Rust 在 Linux 上 UPX 后仍小 1.9×。

### 4.6.1 Go server（linux/amd64, 38,584,480 B）压缩矩阵

| 处理 | 字节 | 占原始 |
|---|---:|---:|
| 原始（用 `build.mjs` 的真实参数：`-trimpath -buildvcs=false -tags production -ldflags "-s -w"`） | 38,584,480 | 100% |
| + `-ldflags "-buildid="` | 38,584,444 | 100.0%（**−36 B，可忽略**） |
| + `-gcflags=all=-l`（禁内联） | 34,566,268 | **89.6%** |
| UPX `-9`（默认最高档，NRV 算法） | 13,042,464 | 33.8% |
| **UPX `--lzma`** | **9,769,520** | **25.3%** |
| gzip -9（对照：容器层自带的就是这个量级） | 13,164,788 | 34.1% |
| bzip2 -9 | 12,120,960 | 31.4% |
| **xz -9（理论地板）** | **9,633,116** | **25.0%** |

两个关键读法：

- **`upx -9` ≈ `gzip -9`**（12.44 vs 12.55 MiB，差 1%）⇒ 在**压缩分发通道**里，默认档 UPX 的收益会被通道自带的 gzip 吃掉。
- **`upx --lzma` 距 `xz -9` 只剩 1.4%** ⇒ 压缩器已经没有可榨空间了；`--ultra-brute` 之类的暴力档最多在这 1.4% 里找食。

顺带一个反直觉但可复现的结果：**禁用内联反而让二进制小 10.4%**。原因是 Go 的内联会在每个调用点复制函数体，禁用后代码重复度下降。代价是函数调用开销（**本次未测吞吐影响**）。

### 4.6.2 分发通道才是判据：拆开我们自己的 LPK

`file` 显示 LPK 是 **POSIX tar**，内部：

```
images/blobs/sha256/3795a4a…   gzip, original size 17,299,968   ← 我们那一层
images/blobs/sha256/647726a…   gzip, original size 24,974,336   ← debian 基线层
content.tar.gz                 gzip, original size  3,006,976
```

**镜像层是 gzip 压缩的**，所以「二进制能不能更小」必须换算成「gzip 后的层能不能更小」：

| 二进制 | 层内原始 | tar + `gzip -9` 后（模拟层大小） |
|---|---:|---:|
| 原始（`lazycat/image/nexterm-server`） | 17,296,752 | 7,229,421 |
| UPX `-9` | 6,850,232 | 6,688,758 |
| **UPX `--lzma`** | **5,163,076** | **5,164,761** |

包内那一层的**实际**大小是 **7,517,202 B**（与模拟的 7.23 MB 差 4%，说明官方用的是默认 gzip 档而非 `-9`，口径吻合，可交叉验证）。

⇒ **用 `--lzma`**：层 7,517,202 → ~5,164,761，**LPK 总量 17,350,656 → ~14,998,215（−2.24 MiB，−13.6%）**
⇒ **用 `-9`**：层只到 ~6,688,758，**−0.79 MiB（−4.8%）**，白赔一次解压开销

**这就是 §4.5 那个「体积反转」结论作废的地方**：我们的 Linux 二进制**同样能 UPX**，而且 `--lzma` 后（5.16 MB 层）比 Go 的（9.32 MiB）小 —— 因为 §4.5 里拿来做对比的 Rust 二进制是 **macOS 产物**，UPX 对它本来就返回拒绝。

### 4.6.3 编译期 / profile：不进压缩器的那一半空间

**Go**（linux/amd64）：见 §4.6.1 前两行 —— 只有 `-gcflags=all=-l` 有量级收益（−10.4%），`-buildid=` 是噪声级。

**Rust**（darwin/arm64, server 模式，两两对照，各自完整重建）：

| profile 设置 | 体积 | 相对现状 |
|---|---:|---:|
| 现状：`opt-level="s"`, `lto="thin"`, `codegen-units=16` | 17,108,880 | — |
| 仅 `opt-level="z"` | 14,942,592 | −12.7% |
| 仅 `lto="fat"` | 12,352,128 | **−27.8%** |
| `opt-level="z"` + `lto="fat"` + `codegen-units=1` | **9,141,664** | **−46.6%** |

- **`lto="fat"` 是单项收益最大的一刀**（−27.8%），`opt-level="z"` 再叠一层，`codegen-units=1` 补上剩余。
- 已验证不是假的：`__TEXT` 从 14,106,624 → 8,159,232（−42%），且二进制**能正常跑**（`--version` → `nexterm-server 0.2.1`）。
- 构建耗时 2m28s（对比冷构建 119s），代价可接受。
- ⚠️ **代价未实测**：`opt-level="z"` 会牺牲向量化等优化，对**终端吞吐**的影响本次没测。进流水线前应先跑一次 `cargo bench --bench terminal_throughput` 做前后对比。
- 本次**没有改动 `Cargo.toml`**：实验全用 `CARGO_PROFILE_RELEASE_*` 环境变量 + 独立 `CARGO_TARGET_DIR`。

同目标下的公平对比（darwin/arm64）：

| | 现状 | 各自调到能调的最优 |
|---|---:|---:|
| Rust | 16.32 MiB | **8.72 MiB** |
| Go | 35.17 MiB | 32.96 MiB（禁内联） |

### 4.6.4 真正的杠杆：依赖体积归因（Go server）

方法：对**未 strip** 的二进制跑 `go tool nm -size`，**排除 BSS 段**（BSS 不占文件体积，否则会被一个符号误导 —— 见 §4.6.5），再按 `go list -deps` 的 585 个真实包名做最长前缀匹配聚合。文件态符号合计 **19.9 MiB**。

| 模块 | 体积 |
|---|---:|
| `github.com/redis/go-redis/v9` | **3.37 MiB** |
| `github.com/cloudwego/eino` | 2.17 MiB |
| `modernc.org/sqlite/lib` | 1.94 MiB |
| **`github.com/ProbiusOfficial/NexTerm`（自研后端全部）** | **1.91 MiB** |
| `golang.org/x/text` | 0.74 MiB |
| `runtime` | 0.57 MiB |
| `crypto/tls` | 0.34 MiB |
| `net/http` / `golang.org/x/crypto` | 0.29 MiB / 0.29 MiB |
| `gonja`（模板引擎） | 0.28 MiB |
| `moby/moby` | 0.22 MiB |
| `json-iterator/go` | 0.19 MiB |
| `encoding/json/v2` | 0.17 MiB |
| 其余 244 个模块 | 3.89 MiB |

两点值得记：

- **`redis/go-redis` 是最大单块，比整个自研后端还大 1.8 倍。** 一个 Redis 客户端不该有 3.37 MiB 代码 —— 它把每个 Redis 模块（JSON / TimeSeries / Bloom / Search / Gears…）的命令集全编进来了。若真要减体积，这是第一个目标。
- **同时链进了三套 JSON**：`bytedance/sonic`（经 eino 引入）+ `json-iterator` + `encoding/json/v2`。收敛成一套是净收益。

### 4.6.5 附带发现：Go 1.26+ 的 32 MiB BSS 保留

`go tool nm -size` 里最大的单个符号是这个：

```
291fb00   33554432 B   crypto/internal/fips140/drbg.memory
```

- 类型 `B` = **BSS**（未初始化段）⇒ **不占文件体积**。这正是上面归因必须先排除 BSS 的原因：不排除的话它会虚报成「crypto 占 33 MiB」，而实际 crypto 只有 1.2 MiB。
- 但它是 Go 1.26 引入的 FIPS DRBG 暂存区，**静态保留 32 MiB 虚拟内存**。
- 上游已立案（**golang/go#81505**）：在低内存 Linux（默认 `overcommit_memory=0`）上，这个保留会让二进制**在 `main()` 之前被内核 SIGKILL —— exit 137，无输出、无 panic、无 core**。该 issue 给出的对照表：go1.25.3 的 BSS 只有 ~100 KB，go1.26.1 / 1.27.1 都是 ~33.7 MB。
- `GOFIPS140=off` 在本机**已是默认**且无差别（实测字节数完全相同），`GOEXPERIMENT=nofips140` 在 1.27.1 上**不存在** ⇒ **目前没有开关能去掉它**（issue 已排进 Go 1.28 里程碑）。
- 影响评估：我们实测 RSS 只有 **15.3 MiB**（BSS 惰性分配，不触碰就不占物理内存）⇒ **对内存正常的机器无影响**；但对**懒猫这类小内存设备**和**低配自建服务器**是一个真实的启动风险，值得在部署文档里写一句。
- ⚠️ **诚实标注**：本条是「**本机符号确认 + 上游 issue 引用**」，**没有在本机复现低内存 SIGKILL 场景**（无法在本机构造）。

### 4.6.6 UPX 后的运行验证：本机无法完成

懒猫产物是 linux/amd64，而宿主是 arm64 ⇒ 容器里走 qemu 模拟。实测发现**同一 qemu 下，所有 UPX 版（包括走成熟路径的 Go 版）都 `Trace/breakpoint trap`**，而未压的原版正常：

| 二进制 | 同 qemu(amd64) 下 `--version` |
|---|---|
| Rust 原始（linux/amd64） | ✅ `nexterm-server 0.2.1` |
| Rust UPX `--lzma` | ❌ Trace/breakpoint trap |
| Go UPX `-9` | ❌ Trace/breakpoint trap |
| Go UPX `--lzma` | ❌ Trace/breakpoint trap |

⇒ 这是 **qemu-user 执行不了 UPX 的解压桩**，不是 UPX 的缺陷（否则 Go 版不会一起挂）。
⇒ **结论：amd64 的 UPX 产物必须在真 amd64 机器（或懒猫盒子）上验一次再进流水线。** §4.5.2 那次 arm64 原生容器验证是可用的，因为它是原生执行、不经 qemu。

### 4.6.7 桌面 / macOS：UPX 用不了，只剩两条路

macOS 上 UPX 直接拒绝（Rust / Go 一样），所以桌面端只能靠：

1. **编译期 / profile** —— 见 §4.6.3，Rust 侧有 −46.6% 的实测空间。
2. **嵌入前端预压缩** —— 桌面二进制把 `dist` 原样 embed。我们这份 dist 是 **1.84 MiB**（最大单文件 `assets/index-*.js` 1.57 MB），`gzip -9` 后 0.66 MiB、`xz -9` 后 0.56 MiB ⇒ 改成「嵌压缩包 + 运行时解压」可省 **~1.2 MiB**（约 3%，且随前端增长同步增长）。

### 4.6.8 建议

1. **懒猫服务端**：上 `upx --lzma`（**不是默认档**）—— LPK 直接小 **2.24 MiB（−13.6%）**。默认档 `-9` 在 gzip 通道里等于白压，还白赔一次解压。
2. **给 Rust 侧换 profile**：先单独上 `lto="fat"`（−27.8%，最小改动最大收益），再评估 `opt-level="z"` + `codegen-units=1`（合计 −46.6%）。**进流水线前务必先跑 `terminal_throughput` 确认吞吐没掉。**
3. **别在压缩器上继续投入** —— 距理论下限只剩 1.4%。
4. **要真减体积就砍依赖**（Go 侧）：`redis/go-redis`（3.37 MiB）是首选；三套 JSON 收敛成一套。
5. **macOS 桌面端**：只有 profile + 前端预压缩两条路；UPX 这条路是死的（UPX 不支持 Mach-O + codesign 校验双重阻断）。

---

## 4.7 构建成本：编译速度与构建产物（受控实测）

**问题**：Go 的「构建快 + 产物少」到底值多少？

**口径**（两侧一致）：全新缓存（Go 用独立 `GOCACHE`，Rust 用独立 `CARGO_TARGET_DIR`）；同目标 **darwin/arm64**；只构建 `nexterm-server` 一个二进制；「增量」= 改一个**深层内部文件的真实内容**（不是 `touch`）。

### 4.7.1 编译速度：必须分成「两个循环」说，否则结论会失真

| 循环 | Rust/Tauri | Go/Wails | |
|---|---:|---:|---|
| **产出生产二进制**：冷构建（全新缓存） | 129.5 s | 18.6 s | Go 快 **7.0×** |
| **产出生产二进制**：改一行后重建（稳态） | **41.4 s**（40.4–43.8） | **2.3–3.5 s** | Go 快 **12–18×** |
| 同上，无改动 | 0.3–0.8 s | — | — |
| **日常改代码**：debug 重建 | 3.1–5.5 s | — | — |
| **日常改代码**：`cargo check`（不产出机器码） | 2.4 s | — | — |
| **日常改代码**：Go（无 debug/release 之分） | — | 0.8–3.5 s | **基本持平** |

⚠️ **这里有一个极容易误导的比法，必须先修正**：拿 Rust 的 `--release`（41 s）对 Go 的 `go build`（2.3 s）得出「Go 快 18 倍」是**不对称**的 —— Go 没有 debug/release 二选一，它只有一种构建（而且已经是优化过的）。真实的**开发循环**是 **2.4–5.5 s vs 0.8–3.5 s，基本持平**。

被真正拉开的是「**产出优化二进制**」这个循环：**每次改一行要 41 秒 vs 2 秒**。对一个「改 → 起服务 → 看行为」的后端调试节奏来说，这就是 40 秒和 2 秒的体感差别。

### 4.7.2 根因：编译单元的粒度，不是「语言快慢」

| | Rust/Tauri | Go/Wails |
|---|---|---|
| 生产代码规模 | 27,345 行 / 74 文件 | 52,517 行 / 45 包 |
| **编译单元** | **1 个 crate（`nexterm_lib`）** | **45 个包（43 internal + 2 main）** |

`cargo build -v` 实测：改任何一行，被重编的永远只有两条 `rustc` 调用 —— `crate-name nexterm_lib` + `crate-name nexterm_server`。也就是**哪怕只 `touch` 一下 bin 入口，27,345 行也要全部重新类型检查 + 代码生成 + ThinLTO 重链接**。

而 `cargo check`（只类型检查、不生成机器码、不链接）单独就要 **30.6 s**（release）—— 说明那 41 s 的成本**主要压在「一个超大编译单元」上，不在链接**。Go 侧一次改动只重编 `internal/terminal` 一个包 + 重链接。

### 4.7.3 构建产物：Go 赢在「记账方式」，不只是「总量」

同一口径（单 profile / 单 target / 单特征集，构建一个服务器二进制）：

| 指标 | Rust/Tauri | Go/Wails | |
|---|---:|---:|---|
| 构建目录原始大小 | 1,564 MB | 574 MB | Rust 大 **2.7×** |
| 文件数 | 5,539 | 3,604 | Rust 多 **1.5×** |
| **gzip -6 后**（= CI 缓存 / 传输的口径） | **515 MB** | **109 MB** | Rust 大 **4.7×** |

但**量级差只是表面，结构性差异更关键**：

| | Rust `target/` | Go `$GOCACHE` |
|---|---|---|
| 作用域 | **项目级**（每仓一份） | **全局单份**（本机所有 Go 项目共用） |
| 维度 | × profile × target triple × 特征集 | 内容寻址，自动去重 |
| 淘汰 | 不自动淘汰，只随 `cargo clean` 清 | LRU 修剪 |
| 项目目录内残留 | 整个构建目录 | **只有产物二进制** |

**本仓真实占用**（`du` 实测）：

```
Rust：
  target/            5.8 GB / 18,641 文件   （debug 2.3 G + release 3.6 G）
  target-linux/      1.8 GB /  6,256 文件   （第二个 target triple：整棵依赖树又编一遍）
  ──────────────────────────────────────
  项目内合计         7.6 GB / 24,897 文件
+ 全局 ~/.cargo/registry 1.5 GB + ~/.cargo/git 52 MB
  ──────────────────────────────────────
  总计              ≈ 9.2 GB

Go：
  项目内              ≈ 0（只有产物二进制）
  全局 $GOCACHE         565 MB
  全局 $GOMODCACHE      682 MB  （同样是全局共用）
  ──────────────────────────────────────
  总计              ≈ 1.25 GB
```

**约 7.3×**，而且这个差距会随「机器上的 Rust 项目数」线性放大 —— Go 那份全局缓存**不随项目数增长**。

两个可验证的「膨胀」证据：

1. **同一 crate 有多份 rlib**：`target/release/deps` 里 **1,253 个 rlib 只对应 435 个 crate 名（平均 2.9 份）**；`hashbrown` **12 份**、`base64` **10 份**、`getrandom` 9 份、`rand` / `md5` / `thiserror` 各 8 份。
2. **反复构建会继续长**：一个全新 target 目录里 `cargo build` / `cargo check` 交替跑几轮后 —— **1,564 → 1,800 MB（+15%）、5,539 → 7,657 文件（+38%）**。同期 Go 缓存 **574 → 615 MB（+7%）、3,604 → 3,632 文件（+0.8%）**。

### 4.7.4 对我们自己的结论（独立于「要不要迁 Go」）

**我们那 41 秒不是 Rust 的宿命，是结构造成的，而且可修。**

1. **拆 `nexterm_lib`**：74 个文件 / 27k 行挤在一个 crate 里 ⇒ 改一行重编全部。目标是把 41 s 打到个位数。
   ⚠️ 拆分必须按「**改动频率 × 下游大小**」设计：把最常改的模块放在依赖树的**最下游**。只把大块拆开但仍放在顶端（被 lib 依赖），收益是零。
2. **短期零风险项**：`src-tauri/build.rs` 当前**没有任何 `cargo:rerun-if-changed`**，Cargo 会退回默认行为（包内任何文件变动都重跑构建脚本）。加一行 `cargo:rerun-if-changed=build.rs` 值得先实测收益。
3. **debug 档 3–5 s 说明 Rust 不是不能快** —— 是 release 档的 ThinLTO + 单体 crate 把它拖住了。日常迭代应该用 debug（`tauri dev` / `cargo build`），把 `--release` 留给交付。
4. **磁盘侧**：`target-linux/` 这种第二个 target triple 的目录建议定期 `cargo sweep` 或放到独立卷；`~/.cargo/registry`（1.5 GB）是共享的，不用管。

> **注**：我怀疑过「`cargo check` 与 `cargo build` 互相失效、每次交替多付 40 秒」，专门做了 6 步交替序列验证 —— **证伪**：紧跟 check 的 build 只要 **0.84 s**。不作为问题记录。

---

## 5. 代码质量评估

### 5.1 值得肯定的（有实测支撑）

| 项 | 证据 |
|---|---|
| 模块边界真实 | `internal/ipc` / `internal/platform` 对业务包 **0 引用**（grep 实测） |
| 代码卫生 | `TODO/FIXME/XXX/HACK` 全仓 **0 处** |
| panic 使用克制 | 生产代码仅 **3 处**，全在 AI 初始化（`ai/tools/eino.go` ×2、`ai/agent/runner.go` ×1） |
| 品牌残留彻底 | 前端 `*.ts/tsx` 里 `Tauri/tauri` **0 处**；Go 代码里 `Rust/rust` 引用 **0 处** |
| 命令注册安全 | 重名注册显式报错；未知命令返回 `CodeNotFound`；`Dispatch` 有 recover 兜底 |
| CI 门禁扎实 | quality：gofmt + go vet + test + **test -race** + production-tag 测试 + bindings drift + 前端可复现构建；native：8 组合真机构建 + smoke + 服务端 e2e |
| **诚实度** | 自评 `feature_complete=false`，**139 条命令只有 2 条算「已验证」**，136 条「已实现未验证」，1 条未实现；规则白纸黑字写着 *"registration alone is never a pass"*、*"generic adapter tests are not production-feature passes"* |

最后一条尤其值得说：**没有人会主动把自己的进度报成 2/139**。这种自评口径比绝大多数「已完成 90%」的汇报可信。

### 5.2 发现的问题（本次实测，未见于其文档）

**① macOS 上 `go test ./...` 会失败 —— 且 CI 永远不会发现**

```
--- FAIL: TestChmodAndAtomicWritePreserveMode (0.02s)
    chmod_unix_test.go:31: mode = 4000751
FAIL	github.com/ProbiusOfficial/NexTerm/internal/fs/local
```

- 复现：`go test ./internal/fs/local/`（与 `-tags production`、`-race` 无关，都失败）
- 根因：测试断言 `chmod` 能设上 **setuid** 位，但 macOS 内核在 `t.TempDir()` 里**忽略了 setuid**；实际拿到 `ModeSticky|0751`，缺 `ModeSetuid`
- 为什么 CI 抓不到：CI 的 `quality` job 跑在 `ubuntu-latest`（Linux 上这个测试过）；而 `native` job 的 macOS/Windows leg **只跑构建 + smoke，不跑单测**。所以这个失败在 macOS/Windows 上会一直躺着
- 影响：这是**测试代码的跨平台假设错误**，不是产品缺陷。但意味着「Go 版在 macOS 上测试全绿」这个说法不成立

**② 工具链必须钉死 Go 1.26.8，否则 JSON 路径静默降级**

我用 Homebrew 的 Go 1.27.1 构建，每次运行都打：

```
WARNING: sonic/ast only supports (go1.17~1.26 ...), but your environment
is not suitable and will fallback to encoding/json
```

`bytedance/sonic`（Go 版的高性能 JSON）在 1.27 上不生效，静默退回 `encoding/json`。这不影响正确性，但**会显著影响 JSON 密集路径的性能**，而我上面的性能数字正是在这个降级状态下测的 —— 即**对 Go 侧是不利的**，真实 1.26.8 下应该更好。朋友把 `toolchain go1.26.8` 钉死是对的。

**③ 文档与实现已经脱节**

- `docs/acceptance-rwig/baseline/README.md` 说「当前 141 方法 / 140 唯一」，且「checked-in 的 inventory.json 仍是 M20 状态（140/139），`--check` 在 HEAD 上会失败」
- 实测：inventory.json 已是 **144 方法 / 143 唯一**，`--check` **通过**（exit 0）
- 结论：`inventory.json` 已被重新生成，但 README 那段警告没跟着改。**文档在自贬，实际状态比文档好**

**④ 命令面数字在文档里已经滞后**

- 朋友 baseline 记的是「full server **140** 命令」
- 我在 HEAD `ce84b62` 上实测 `/healthz` = **155** 条
- 说明分叉后 Go 侧又加了 15 条（主要是 AI 相关）。任何引用固定命令数的文档都会随 HEAD 漂移 —— 这一点我们自己也有同样的病（记忆里 121→132→133→139 的漂移史）

**⑤ ⛔ 部署在反向代理后面会整体不可用 —— `transportGuard` 的 Origin 校验把自己也挡了（阻断级，本轮实测）**

`https://nextermgo.lazycore.heiyu.space/` 打开是**白屏**（`#root` 子节点数 0）。真因不在前端：`/assets/*.js` 与 `/assets/*.css` 全被 **403 `origin is not allowed`**，于是入口模块从未执行。**而且不只是白屏** —— 连 `/rpc` 也一起 403，等于整套在反代后面不可用。

完整因果链（每一环都有实测）：

| # | 环节 | 证据 |
|---|---|---|
| 1 | guard 包住**全部路由，含静态资源** | `internal/server/server.go:142`：`s.handler = s.transportGuard(s.routes(config))` |
| 2 | 只要请求带 `Origin` 就必须过白名单，否则 403 | `transport_security.go:17-22`，body 正是 `origin is not allowed`（22 B） |
| 3 | 唯一的「同源豁免」是 `EqualFold(parsed.Host, r.Host)` | `transport_security.go:60` |
| 4 | **反代会改写 `r.Host`** ⇒ 豁免永不命中 | `Origin: http://nexterm-server:8080` → **200**；`Origin: https://nextermgo.lazycore.heiyu.space` → **403**。⇒ 容器里看到的 `r.Host` 是后端服务名。且 `nexterm-server:8080` 并不在默认白名单（只有 `localhost:*`/`127.0.0.1:*`）里却通过了 ⇒ 只能走同源那条 ⇒ 结论闭合 |
| 5 | Vite 产物给入口带 `crossorigin` ⇒ 浏览器**必带 `Origin`** | `<script type="module" crossorigin src=…>` + `<link rel="stylesheet" crossorigin …>` |
| 6 | ⇒ 入口 JS/CSS 403 ⇒ `#root` 空 ⇒ 白屏 | CDP 抓到 `net::ERR_ABORTED`、`Refused to apply style … MIME type ('text/plain')`、`rootChildren: 0` |

排除法与边界：

- **不是懒猫网关的锅**：容器内 `wget --header="Origin: http://x" http://nexterm-server:8080/assets/…` **同样 403** ⇒ 是应用自己返回的。
- **不是懒猫特有**：任何改写 `Host` 的反代（nginx `proxy_set_header Host $proxy_host`、compose 服务名、K8s）都会触发。
- **只在反代后面出现**：`localhost:PORT` 直连时 `Origin` 与 `r.Host` 一致 ⇒ 豁免命中 ⇒ 上游 CI 与本地开发**全绿**，缺陷藏得住。
- **Rust 线不受影响**：`src-tauri/` 里 grep `Origin` / `allowed_origin` **零命中**，我们那边根本没有这层校验。
- **命令面之外，这个 guard 也管 `/rpc`**：浏览器同源 `fetch` 的 POST **也带 `Origin`** ⇒ 实测带 Origin 403、不带 Origin 200（拿到真实 `forward_env` JSON）。

引入与影响面：

- 引入提交 **`e45c610 fix(server): harden browser transport and shutdown`**（2026-10-02，HydrogenE7），附带 `transport_security_test.go` 126 行 —— 但那些测试**全在 localhost 上跑**，「Host 被反代改写」这一维没有覆盖。
- 该提交**已在 `origin/rwig` 上** ⇒ **release v0.2.2-rc.1 同样含此缺陷**：若照原样上架懒猫商店，普通用户打开同样是白屏。

修复方向（三选，需组合）：

| 方案 | 效果 | 依赖 |
|---|---|---|
| a. 同源判定同时接受 `X-Forwarded-Host` / `Forwarded` | 根治（静态资源 + `/rpc` + `/ws/*` 一起修好） | ✅ **已实测：懒猫网关下发 `X-Forwarded-Host`**（下方证据）⇒ 充分解，无需任何配置 |
| b. 把静态资源排除在 Origin 校验之外 | 只修白屏（零依赖、零风险，静态资源本无副作用） | 修不了 `/rpc`、`/ws/*`，功能仍是死的 |
| c. 给 `AllowedOrigins` 开配置入口（flag / env） | 让部署方能显式把公网域加进白名单 | `cmd/nexterm-server/main.go` 现在**完全不传该字段**，默认只有 `localhost:*`/`127.0.0.1:*` |

⛔ 单改 (c) **不够**（静态资源也走 guard）；单改前端去掉 `crossorigin` **也不够**（`/rpc` 仍 403）。最小可用组合是 **(b) + (c)**；最干净的是 **(a)**。

### 5.3 ✅ 已在本仓（`local/lazycat-go-dev` 分支）落地修复并实机验收

提交 `ca7a713`，改了 8 个文件（+279 −13）：

| 文件 | 改动 |
|---|---|
| `internal/server/transport_security.go` | 同源判定兼收 `X-Forwarded-Host`（多值/逗号列表都解）与 RFC 7239 `Forwarded` 的 `host=`；被拒请求补一条 `WARN`，把全部候选 host 打出来 |
| `internal/app/cli.go` | `--allowed-origin`（可重复）+ `NEXTERM_ALLOWED_ORIGINS`（逗号/空白分隔），进 `Invocation` |
| `internal/server/config.go` / `cmd/nexterm-server/main.go` | 把上面两项接到 `server.Options.AllowedOrigins`（原先这个字段在 `main.go` 里根本没接线） |
| `internal/server/transport_security_test.go` | +3 个回归测试（转发头放行 / 恶意 Origin 仍拦 / 配置白名单放行）——**回退源码即失败（403）**，证明测试真能抓住这个洞 |
| `internal/app/cli_test.go` | +1 个 CLI 解析测试（env、两种 flag 形式、未设时保持 nil 以保留默认值） |
| `lazycat/lzc-manifest.yml` | `NEXTERM_ALLOWED_ORIGINS=https://{{ .S.AppDomain }}` —— 用**部署期渲染**的实例域名，不写死；换盒子自动跟着走 |
| `lazycat/image/Dockerfile` | 补 `tmux`（见 5.4） |

**关键未知量的答案**：懒猫网关**确实下发 `X-Forwarded-Host`**。判据是一条只带 `Origin`、不带任何转发头的公网请求，在服务端日志里留下了两个候选 host：

```
curl -H "Lzc-Auth-Token: $TOK" -H 'Origin: https://evil.example' \
     https://nextermgo.lazycore.heiyu.space/assets/index-DDzVmQ83.js   # → 403

level=WARN msg="rejected request origin" origin=https://evil.example
  host_candidates=nexterm-server:8080,nextermgo.lazycore.heiyu.space
```

第二个候选只可能来自网关（curl 没发）⇒ 网关改写 `Host` 的同时用 `X-Forwarded-Host` 保留了对外域名 ⇒ **方案 (a) 单独就够**。

**实机验收（`https://nextermgo.lazycore.heiyu.space`）**：

| 项 | 修复前 | 修复后 |
|---|---|---|
| `/assets/index.js` 带 `Origin=公网域` | 403 `text/plain` | **200 `text/javascript`（1,665,336 B）** |
| `/assets/index.css` 带 `Origin=公网域` | 403 | **200 `text/css`（89,392 B）** |
| `POST /rpc` 带 `Origin=公网域` | 403 | **200 + 真实 JSON**（`{"ok":true,"data":{"available":false,"platform":"lazycat",…}}`） |
| `GET /ws/events` 带 `Origin=公网域` | 403 | **101 Switching Protocols** |
| 任意路径带 `Origin=https://evil.example` | 403 | **403（守卫没被削弱）** |
| 真浏览器（CDP）`#root` | `rootChildren: 0`（白屏） | **`rootChildren: 1`、16,633 B HTML、无失败请求、无异常** |
| 点「打开本地终端」后敲 `echo …` | — | 服务端 `tmux capture-pane` 与浏览器画面**逐字一致** ⇒ 输入+回显全双工串通 |

`/ws/events` 这一条值得单独说：**浏览器发起 WebSocket 握手一定会带 `Origin`**，所以这个缺陷在懒猫上不只毁掉页面，还把终端流一起封死了；修复前那 403 是必然的。

### 5.4 ⚠️ 顺带查出的部署级缺口：懒猫镜像里没有 `tmux`

修完 Origin 之后点「打开本地终端」，得到的是：

```
[attach 失败] unsupported: durable terminal unavailable:
  tmux executable "tmux": exec: "tmux": executable file not found in $PATH
```

不是前端问题，也不是 Origin 问题 —— **终端会话全部建立在 tmux 之上**（`internal/durable` 的 tmux 后端），而 `lazycat/image/Dockerfile` 只装了 `ca-certificates / openssh-client / sshfs / fuse3 / tzdata`。

而且上游**刻意没有易失降级路径**：`internal/app/production/durable_production_test.go` 里有一个测试专门用假 tmux 二进制（`DurableBinary: "nexterm-no-such-tmux-binary"`）断言 `terminal_attach` 返回 `CodeUnsupported`，并显式断言「volatile fallback **不得**开流」。⇒ tmux 是**运行期硬依赖**，不是可选增强。

补上 `tmux` 后镜像层只从 22.42 MiB 涨到 **22.95 MiB**（+0.53 MiB）。这是「本地终端」以及 manifest 里那句「关掉网页不会结束会话、换一台设备打开即可接管原来那条终端」能成立的前提。桌面版不受影响（macOS 上 `brew install tmux` 是用户自己的事），但**任何容器化分发都得自己把 tmux 装进去** —— 这条值得写进它的部署文档。

---

## 6. 建议

**短期（看，不动）**

1. 把 `rwig` 当作**独立产品线**看待，不要试图 merge 回来 —— 它是 Go 生态，和我们的 Rust 生态没有共享代码（连 `src-tauri/` 都没了）。
2. 如果想要它里面的**功能改进**（前端那 43k 行里有很多我们没有的 AI 能力：`conversation/steer/hitl/subagent`），应当**按功能点逐个搬**，而不是整体接受。

**中期（可以借鉴到我们这边）**

3. **`internal/ipc` 的分层方式值得抄**：`Register` / `RegisterNested` 把「嵌套参数」这个我们靠注释和记忆维护的坑，变成了类型系统里的显式选择。我们的 `nexterm_commands!` 宏 + `Ctx::arg` 同样能承载这个语义。
4. **命令面对账脚本值得抄**：朋友用「冻结基线 JSON + 实时扫描 + 差额必须逐条解释」守住命令面，比我们靠 `/healthz` 数数字 + 人工比对可靠。
5. **`test -race` 值得加进我们的 CI**：我们现在的 Rust 测试没有对应的并发竞态检测手段。
6. **顺手做掉我们自己的构建税**（§4.7.4）：把 `nexterm_lib` 按「改动频率 × 下游大小」拆 crate，目标是 `--release` 增量从 41 s 降到个位数；并给 `build.rs` 补 `cargo:rerun-if-changed`。**这件事与「迁不迁 Go」无关，做了就赚。**

**需要警惕的**

7. 若考虑迁移到 Go，先想清楚**内存翻倍**能不能接受 —— 懒猫微服和自建服务端都是常驻场景，2.1× 内存是真实成本。
   **但构建成本是 Go 的真实优势，且比我上一版报告写的更值钱**（§4.7）：产出优化二进制的循环是 **18.6 s / 2.3 s vs 129.5 s / 41.4 s**，构建目录 **574 MB vs 1,564 MB**，且 Go 的缓存是**全局共享**的（不随项目数增长，本仓口径合计 1.25 GB vs 9.2 GB）。对一个需要频繁出包的运维工具，这是日常体感差别，不是纸面数字。
   **体积不是迁移理由（修正后）**：同口径下，Rust 在 Linux 上 UPX 后仍小 1.9×，在 macOS 上小约 4×（含 Rust 换 profile 的收益，见 §4.6.3）；Go 侧的体积优势只存在于「双方都不做任何优化」这个不成立的假设里。
8. 别被「139 条命令都已实现」的表面数字骗到 —— 朋友自己标注的是 **2/139 已验证**。功能面对齐 ≠ 功能可用。

---

## 附录：本次实测的原始命令

```bash
# 拉取与定位
git fetch --all --prune
git worktree add /Users/macmini/nexterm-wt/rwig origin/rwig --detach

# Go 侧（Go 1.27.1，CGO_ENABLED=0，-trimpath -ldflags "-s -w" -tags production）
export GOCACHE=<worktree>/target/gocache-cold && rm -rf "$GOCACHE"
/usr/bin/time -p go build -trimpath -ldflags "-s -w" -tags production \
  -o /tmp/gobuild/nexterm-server-darwin-arm64 ./cmd/nexterm-server     # 19.80s
GOOS=linux GOARCH=amd64 /usr/bin/time -p go build ... -o ...linux-amd64  # 18.65s

# Go 运行态
du /tmp/gobuild/*                                  # 36,879,026 / 38,584,480 B
curl -sf http://127.0.0.1:18080/healthz            # 114 ms 到首次 200；"commands":155
vmmap -summary <pid> | grep "Physical footprint"   # 15.3M

# Go 测试与基准
go test -tags production ./...                     # 52.2s；44 包过，fs/local 失败
go test -run=^$ -bench=. -benchtime=2s ./internal/terminal/   # FeedASCII 69.68 MB/s

# Rust 侧（1.98.1）
cargo build --release -p nexterm --no-default-features --features server \
  --bin nexterm-server                             # 冷 119.0s / 热 0.81s
cargo test --workspace --no-default-features --features server   # 301 passed / 0 failed
cargo bench -p nexterm --bench terminal_throughput # 80.8 MB/s UTF-8

# Rust 运行态
curl -sf http://127.0.0.1:18082/healthz            # 370 ms；"commands":139
vmmap -summary <pid> | grep "Physical footprint"   # 7312K
```

### §4.6 追加的原始命令

```bash
# 权威基线（复刻 scripts/build.mjs 的参数，而不是我自己拼的）
cd /Users/macmini/nexterm-wt/rwig
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath -buildvcs=false \
  -tags production -ldflags "-s -w" -o /tmp/gobi/A0-base ./cmd/nexterm-server        # 38,584,480

# 编译期变体
... -ldflags "-s -w -buildid="                 -o /tmp/gobi/A1-nobuildid ...   # 38,584,444
... -ldflags "-s -w -buildid=" -gcflags=all=-l -o /tmp/gobi/A2-noinline  ...   # 34,566,268
... （不带 -s -w）                                -o /tmp/gobi/A3-syms     ...   # 55,718,537（供 nm 归因）

# 压缩矩阵（UPX 5.2.1）
upx -9     -o /tmp/upxopt/t_9    /tmp/gobuild/nexterm-server-linux-amd64   # 13,042,464
upx --lzma -o /tmp/upxopt/t_lzma /tmp/gobuild/nexterm-server-linux-amd64   #  9,769,520
python3 -c "import gzip,lzma; d=open('.../nexterm-server-linux-amd64','rb').read();
print(len(gzip.compress(d,9)), len(lzma.compress(d,preset=9|lzma.PRESET_EXTREME)))"
# gzip -9 → 13,164,788 ；xz -9 → 9,633,116（理论地板）

# 依赖体积归因（必须先排除 BSS，否则 crypto 会被虚报成 33 MiB）
go list -deps ./cmd/nexterm-server > /tmp/pkgs.txt
go tool nm -size /tmp/gobi/A3-syms | python3 /tmp/agg2.py /tmp/pkgs.txt

# LPK 拆解（确认镜像层是 gzip）
tar -xf lazycat/cloud.lazycat.app.nexterm-v0.2.0.lpk -C /tmp/lpk-x
file /tmp/lpk-x/images/blobs/sha256/*          # gzip, original size 17,299,968 / 24,974,336
ls -l /tmp/lpk-x/images/blobs/sha256/          # 7,517,202（我们那层）/ 8,775,207（基线层）

# LPK 层模拟：tar + gzip -9
upx --lzma -o /tmp/upxopt/rust-linux-lzma lazycat/image/nexterm-server   # 5,163,076
cd /tmp/lpksim && for f in raw nrv lzma9; do tar -cf - "$f" | gzip -9 -c | wc -c; done
#   raw 7,229,421 ｜ nrv 6,688,758 ｜ lzma9 5,164,761

# Rust profile 归因（用环境变量，不改 Cargo.toml）
export PATH="$HOME/.cargo/bin:$PATH"
CARGO_TARGET_DIR=/tmp/rustz-opt CARGO_PROFILE_RELEASE_OPT_LEVEL=z \
  cargo build --release -p nexterm --no-default-features --features server --bin nexterm-server
#   z-only        14,942,592
#   fat-only      12,352,128  (CARGO_PROFILE_RELEASE_LTO=fat)
#   z+fat+cgu=1    9,141,664  (+ CARGO_PROFILE_RELEASE_CODEGEN_UNITS=1)

# amd64 UPX 运行验证（本机不可行：qemu 跑不了 UPX 桩）
docker run --rm -v ~/rustupx:/b debian:bookworm-slim \
  sh -c 'sh /b/t.sh /b/raw 19101; sh /b/t.sh /b/lzma9 19102; sh /b/t.sh /b/go-amd64-9 19103'
#   未压 ✅ ／ 三个 UPX 版全部 Trace/breakpoint trap ⇒ qemu 限制，非 UPX 缺陷
```

### §4.7 追加的原始命令（构建成本，受控口径）

```bash
# ── 口径：双方都用全新缓存、同目标 darwin/arm64、只构建 server 一个二进制 ──

# Go：全新 GOCACHE
cd /Users/macmini/nexterm-wt/rwig
export GOCACHE=/tmp/goart2/cache GOFLAGS=-mod=readonly
/usr/bin/time -p env CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false \
  -tags production -ldflags "-s -w" -o /tmp/goart2/nexterm-server ./cmd/nexterm-server
#   冷构建 18.56 s ；缓存 574 MB / 3,604 文件 ；gzip -6 后 109 MB

# Go：稳态增量必须「改真实内容」——`touch` 只改 mtime，Go 用内容哈希，会 0 编译
f=internal/terminal/doc.go; cp "$f" /tmp/o.bak
printf '\nvar probe = "p"\n' >> "$f" && sleep 1.1
/usr/bin/time -p env CGO_ENABLED=0 go build ... ./cmd/nexterm-server    # 2.3–3.5 s
cp /tmp/o.bak "$f"     # 还原后 git status 必须为空

# Rust：全新 CARGO_TARGET_DIR
export PATH="$HOME/.cargo/bin:$PATH" CARGO_TARGET_DIR=/tmp/rustart/target
/usr/bin/time -p cargo build --release -p nexterm --no-default-features --features server --bin nexterm-server
#   冷构建 129.5 s ；target 1,564 MB / 5,539 文件 ；gzip -6 后 515 MB

# Rust：稳态增量（改一行 → 重编 nexterm_lib + 重链接）
touch src-tauri/src/commands/mod.rs
/usr/bin/time -p cargo build --release -p nexterm --no-default-features --features server --bin nexterm-server
#   41.4 s（40.4–43.8）；无改动 0.3–0.8 s

# Rust：看真实重编了哪些编译单元
cargo build --release ... -v > /tmp/a.log 2>&1
grep -o "crate-name [a-z_]*" /tmp/a.log | sort | uniq -c
#   1 crate-name nexterm_lib ／ 1 crate-name nexterm_server  ← 只有两个

# Rust：debug 档开发循环（公平口径，Go 没有 debug/release 之分）
cargo check -p nexterm --no-default-features --features server --bin nexterm-server   # 2.4 s
cargo build -p nexterm --no-default-features --features server --bin nexterm-server   # 3.1–5.5 s

# 反例排除：check 与 build 是否互相失效？→ 证伪
cargo build --release ...   # 0.84 s（紧跟 check 之后）
cargo check --release ...   # 0.39 s

# 产物统计
du -sh target target-linux ; find target -type f | wc -l
tar -cf - -C /tmp/rustart/target . | gzip -6 | wc -c          # 515 MB
ls target/release/deps/*.rlib | sed 's|.*/||;s|\.rlib$||;s|-[0-9a-f]\{16\}$||' | sort | uniq -c | sort -rn
#   1,253 个 rlib ／ 435 个唯一 crate 名（hashbrown 12 份、base64 10 份）

# 陷阱：zsh 不对未加引号的变量做分词 —— `B="cargo build ..."; $B` 会静默空跑（real 0.00）
#       必须用函数或 sh -c '...'
```

