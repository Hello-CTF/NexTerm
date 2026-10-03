# NexTerm 跨平台规范（Windows / macOS）

> **历史文档（Tauri/Rust 时代），按证据保留，不再代表现行架构。**
> 本文写于 `src-tauri/` 仍存在、桌面壳为 Tauri v2 的时期；文中所有 `cargo` / `tauri` 命令、
> `src-tauri/src/...` 路径、`tauri.conf.json` / `tauri.<platform>.conf.json`、NSIS/DMG 发布矩阵
> 均为当时实况的记录，不能再照跑。`src-tauri/` 整树已删除（`e3235e8`
> 「chore(parity): freeze Rust registry baseline and remove the src-tauri tree」），
> 安装包图标已迁往 `public/brand/`（`4416211` / `e28f544`）。
> **现行架构是 Go**：桌面壳 = Wails v3（`cmd/nexterm-desktop`，前端经 `@wailsio/runtime` 桥接），
> 服务端 = `cmd/nexterm-server`，前端仍是 `src/`（React + Vite）。
> 平台门控的**思路**（§2 的五选一、§4 的提交前清单）仍有参考价值，具体手段以 Go 代码为准；
> §6 的打包/签名/Spotlight 各节保留的是 Tauri 时代的排障证据。

> 目的：让「改一侧、撑死另一侧」这类暗坑在**提交前**被抓住，而不是等对方在另一台机器上白屏。
> 适用：`src-tauri/`（Rust 内核）+ `src/`（React 前端）+ `scripts/` + `.github/workflows/`。
> 最后核对：2026-09-29（macOS 适配分支提交前审计）。

---

## 0. 结论：本次 macOS 适配改动对 Windows 的影响

审计对象 = 全部未提交改动（37 个文件）。方式 = 用 `x86_64-pc-windows-gnu` 目标在 macOS 上真编一遍
Windows 分支（见 §5），跑 CI 同款命令。

| 项 | 判定 | 证据 |
|---|---|---|
| `#[cfg(windows)]` / `cfg!(windows)` 分支编译 | ✅ 通过 | `cargo clippy --target x86_64-pc-windows-gnu --all-targets --all-features -- -D warnings` → `Finished` |
| Windows 侧行为（`net use` 映射盘、自绘三键、DPAPI、PowerShell 本机 shell） | ✅ 无改动 | 平台差异都走 `cfg!` 运行时分支，Windows 走的是原来那条 |
| 前端首屏平台判定 | ✅ 正确 | `app_platform()` 返回 `std::env::consts::OS`，Windows 下 `"windows"` → `isMac()=false` |
| 能力开关（磁盘挂载） | ✅ 正确 | `mount_capability()` 在 Windows 返回 `null` → 面板正常渲染表单 |
| 测试用例跨平台性 | ⚠️ **发现 2 处、已修** | 见 §3 |
| 构建脚本跨平台性 | ⚠️ **发现 3 处、已修** | 见 §3 |
| 锁文件 / pnpm 口径 | ✅ 通过 | `pnpm install --frozen-lockfile` → `Already up to date`（CI 用的就是这个） |
| 前端质量门 | ✅ 通过 | `pnpm typecheck` / `pnpm lint` / `pnpm build` 全绿 |

**结论：修掉 §3 的 5 处后，本次改动对 Windows 构建是零影响的；不修则 Windows CI 必红。**

---

## 1. 平台能力矩阵（唯一事实来源）

写代码时先查这张表：某项能力在某个平台上到底有没有、由谁判定。

| 能力 | Windows | macOS | Linux | 判定位置（Rust） |
|---|---|---|---|---|
| 终端 / SSH / SFTP / 端口转发 / Docker / DB / AI | ✅ | ✅ | 未验收 | — |
| 免主密码凭据保护 | DPAPI | 登录钥匙串 | ❌ 明确报不支持 | `vault/dpapi.rs`（三平台各一个实现） |
| 磁盘挂载 | `net use` 映射盘 | ❌ 暂不可用（缺 macFUSE） | `sshfs` / `fusermount` | `fs/mount.rs::unavailable_reason()` |
| 本机 shell | `pwsh.exe` → `powershell.exe` | `$SHELL` | `$SHELL` → `/bin/sh` | `transport/local.rs::default_shell()` |
| 本机 shell 调用形式 | `-NoLogo -NoProfile -Command` | `-lc` | `-lc` | `transport/local.rs::build_shell_command()` |
| SSH Agent 认证 | ❌ 明确报不支持 | `SSH_AUTH_SOCK` | ✅ | `transport/ssh.rs`（`#[cfg(unix)]` / `#[cfg(not(unix))]`） |
| 文件 chmod | ❌ 无此方法 | ✅ | ✅ | `transport/local.rs` |
| 窗口标题栏 | 自绘最小化/最大化/关闭 | 原生红绿灯 | — | `lib.rs` + `tauri.macos.conf.json` |
| 安装包形态 | NSIS `.exe` | `.app` + `.dmg` | — | `tauri.conf.json` / `tauri.macos.conf.json` |

**图例**：✅ 已实机验收｜❌ 明确不支持（有可读报错，不是静默失败）｜「未验收」= 能编译但没在真机跑过。

---

## 2. 门控规范：照这个写就不会误伤另一侧

按「差异的性质」选手段，五选一：

| # | 差异性质 | 手段 | 本项目实例 |
|---|---|---|---|
| 1 | **依赖**只在某平台存在 | `[target."cfg(...)".dependencies]` | `security-framework` / `objc2-app-kit`（macOS）、`windows`（Windows） |
| 2 | **整块实现**是平台专属 | `#[cfg(target_os = "macos")] pub mod xxx;` | `vault/keychain.rs`（只在 macOS 声明） |
| 3 | **同一接口**每平台一份实现 | 每平台一个同名函数，签名一致 | `vault/dpapi.rs`：`protect`/`unprotect` 三版本（`#[cfg(windows)]` / `#[cfg(target_os="macos")]` / 其余） |
| 4 | **只是一行行为不同** | `cfg!(...)` 运行时分支，**函数本身不加门控** | `fs/mount.rs`：`if cfg!(windows) { mount_windows(..) } else { sshfs }` |
| 5 | **能力开关**要给前端 | 后端出 `*_capability` 命令，前端只呈现不判断 | `commands/mount.rs::mount_capability()` + `src/app/capabilities.ts` |

### 为什么第 4 条要「不加门控」

`cfg!` 是**运行时布尔**，`if cfg!(windows) { a() } else { b() }` 里 `a` 和 `b` **两边都会被编译**。
如果给 `mount_windows` 加 `#[cfg(windows)]`，macOS 上就会报 `cannot find function`。
代价是 Windows 专用函数在所有平台上都会过一遍类型检查 —— 这正是我们要的（编译期就能发现改名、签名漂移）。
函数内部的平台 API 调用（如 `creation_flags`）再单独用 `#[cfg(windows)]` 圈住即可。

### 反例：不要这么写

- ❌ 前端 `isMac()` 猜能力 → 依赖缺失是**本机**状态（macOS 挂载要 macFUSE），不是 OS 属性。走第 5 条。
- ❌ 用 `#[cfg]` 门控一个被 `cfg!` 分支引用的函数 → 编译不过。
- ❌ 平台专属的**测试**用 `#[allow(dead_code)]` 盖住另一侧的告警 → 守护会静默消失（见 §3 第 2 条）。
- ❌ 测试里写死 Unix 命令（`seq` / `cat` / `grep` / 路径分隔符）→ 另一侧必红（见 §3 第 1 条）。

---

## 3. 本次审计抓到并修掉的 5 处

### Rust 侧（会导致 Windows CI 硬红）

| # | 文件 | 症状 | 根因 | 修法 |
|---|---|---|---|---|
| 1 | `src-tauri/src/transport/mod.rs` | 用例 `local_exec_keeps_full_output` 在 Windows 必挂 | 命令写死 `seq 1 1500`；`LocalTransport::exec` 走**本机 shell**，Windows 侧是 PowerShell，没有 `seq` | 按平台取命令：`if cfg!(windows) { "1..1500" } else { "seq 1 1500" }`，并把命令写进断言消息 |
| 2 | `src-tauri/src/commands/mount.rs` | `cargo clippy -- -D warnings` **编译失败**：`function audit_payload is never used` | 该 helper 只被 macOS 门控的用例用到，Windows/Linux 上成了死代码 | 给 helper 加 `#[cfg(target_os = "macos")]`（**不是** `allow(dead_code)`，否则「用例被误门控掉」也没人发现） |

复现证据（修前 → 修后）：

```text
# 修前
error: function `audit_payload` is never used
   --> src-tauri/src/commands/mount.rs:168:14
    = note: `-D dead-code` implied by `-D warnings`
error: could not compile `nexterm` (lib test) due to 1 previous error

# 修后
    Finished `dev` profile [unoptimized + debuginfo] target(s) in 12.59s
```

### 构建脚本侧（`scripts/build.mjs`）

| # | 症状 | 根因 | 修法 |
|---|---|---|---|
| 3 | macOS 上 `node scripts/build.mjs` 在 agent 会话里**直接退出** | 判据是「路径名里含 `temp`」—— `%TEMP%` 字面含 TEMP，但 macOS 的 `/tmp/NexTerm-build` 不含 → 误判越界 | 判据改为「落在临时目录**里面**」：`path.resolve(targetDir).startsWith(path.resolve(TEMP) + path.sep)` |
| 4 | macOS 上临时目录兜底走 `/tmp`（共享目录） | `TEMP ?? TMP ?? "/tmp"`，macOS 两个变量通常都没设 | 兜底改 `os.tmpdir()`（macOS 走 `TMPDIR` 的每用户私有目录） |
| 5 | PATH 里拼出 `undefined\.cargo\bin`，找不到 cargo 时还提示 `%USERPROFILE%` | 写死 Windows 的 `process.env.USERPROFILE` 和 `;` | 改 `path.join(os.homedir(), ".cargo", "bin")` + 平台分隔符；`--install`（Windows 专用）提前到参数校验段、非 Windows 直接拒绝 |

复现证据（修前 → 修后）：

```text
# 修前：CODEBUDDY_SESSION_ID=probe node scripts/build.mjs debug --skip-frontend
  target-dir= /tmp/NexTerm-build
✗ agent 沙箱下 target-dir 必须在 %TEMP% 里，当前是 /tmp/NexTerm-build。

# 修后：同一命令
  target-dir= /var/folders/zh/…/T/NexTerm-build        ← 判据通过，继续往下走
✗ 找不到 cargo。把 /nonexistent/.cargo/bin 加进 PATH 再试。   ← 报错也是跨平台口径了
```

---

## 4. 提交前检查清单

改动碰到平台相关代码时，**逐条**过：

- [ ] 新增的 `#[cfg(...)]` 块，另一侧是否仍有可编译的实现？（第 4 条范式：函数不加门控）
- [ ] 新增的平台专属依赖，是否放进 `[target."cfg(...)".dependencies]` 而不是 `[dependencies]`？
- [ ] 新增的测试用例，命令行/路径/权限假设是否只在某一个平台成立？
- [ ] 新增的 helper 是否只被**门控测试**用到？→ 会触发 `-D warnings` 的 `dead_code`。
- [ ] 前端新加的平台分支，是否读的是后端 `app_platform` / `*_capability`，而不是 `navigator.platform` 猜？
- [ ] `tauri.conf.json` 的改动，另一平台是否在 `tauri.<platform>.conf.json` 里有对应覆盖？
- [ ] 跑一遍 §5 的双平台验证。

---

## 5. 双平台验证怎么跑

### A. 本机（macOS）跑另一侧：交叉编译检查

macOS 上**无法**产出 Windows 安装包，但**可以**把 Windows 分支真编一遍。这套在本次审计里抓到了 §3 的两个 Rust 问题。

```bash
# 一次性准备（约 1~2 分钟）
brew install mingw-w64
rustup target add x86_64-pc-windows-gnu

# 每次改完 Rust 后跑（CI 的 clippy 同款加严）
cd src-tauri
CARGO_TARGET_DIR=/tmp/nx-wingnu \
  cargo clippy --target x86_64-pc-windows-gnu --all-targets --all-features -- -D warnings
```

**为什么是 `-gnu` 而不是 `-msvc`**：`x86_64-pc-windows-msvc` 在 macOS 上过不了依赖里的 C 代码 ——
`ring` 的构建脚本会调 `cc --target=x86_64-pc-windows-msvc`，而本机没有 Windows SDK 头文件，
直接死在 `fatal error: 'assert.h' file not found`。那是**环境限制，不是代码问题**，别被它误导。
`-gnu` 用 mingw 工具链，`cfg(windows)` 同样为真，能覆盖本项目所有平台分支。

**这个手段的能力边界**（别过度相信）：

| 能验 | 不能验 |
|---|---|
| `cfg(windows)` 分支的类型/借用/名字解析 | MSVC 专属行为（`link.exe`、`#[link]` 到系统库、PDB） |
| `-D warnings` 下的 clippy lint | 任何**运行期**行为（真跑测试、真窗口、真 ConPTY） |
| 依赖树在 Windows 上能否编译到 rlib | 打包（NSIS 只能在 Windows 上生成） |

### B. 交给 CI：双平台矩阵

`.github/workflows/ci.yml` 已经在 `windows-latest` + `macos-latest` 上各跑一遍
`fmt` / `clippy -D warnings` / `test` / `typecheck` / `lint`。**判据是：两侧都不许红。**
它才是跨平台行为的最终把关者 —— 本机的交叉编译只是把它提前到提交之前。

---

## 6. 发布流水线的现实约束

**Tauri 不能跨平台打包。** Windows 的 NSIS 安装器必须在 Windows 上产出，macOS 的 `.app` / `.dmg`
必须在 macOS 上产出。所以发布流水线必须是 matrix + 各自出各自的原生产物，不能在 macOS 上「顺手」出一个 exe。

| 平台 | 配置来源 | 产物 | `--install` 捷径 |
|---|---|---|---|
| Windows | `tauri.conf.json`（基础） | NSIS `.exe`（图标 `icon.ico`，`downloadBootstrapper`） | ✅ 覆盖 `%LOCALAPPDATA%\NexTerm\nexterm.exe` |
| macOS | 基础 + `tauri.macos.conf.json` 覆盖 | `.app` + `.dmg`（图标 `icon.icns`） | ❌ 不适用 |
| Linux | 基础（`targets: ["nsis"]`，会失败） | — | 未纳入 |

平台专属配置走 `tauri.<platform>.conf.json`，Tauri 只在对应平台自动合并。
注意 **JSON 数组合并是「整体替换」不是「追加」**，所以 macOS 覆盖里要把整个 `windows` 数组和
整个 `bundle.icon` 重新写全。

### 6.1 出包：打 tag 即出（`.github/workflows/release.yml`）

| 触发 | 平台 | 命令 | 产物 |
|---|---|---|---|
| push `v*` tag | `macos-latest` | `tauri build --bundles app,dmg` | `NexTerm_<ver>_aarch64.dmg` |
| push `v*` tag | `windows-latest` | `tauri build --bundles nsis` | `NexTerm_<ver>_x64-setup.exe` |

产物由 `tauri-apps/tauri-action` 挂到同名 Release（默认 `releaseDraft: true`，检查后再 Publish）。
矩阵里**不要**给某一侧配 `--bundles` 之外的自家专属参数 —— 两侧走各自的平台覆盖文件就够了。

本机要验证打包、且不想等 CI 时：

```bash
pnpm tauri build                      # 自动合并 tauri.macos.conf.json，出 .app + .dmg
pnpm tauri build --bundles app        # 只出 .app（跳过后面的 dmg 步骤）
```

> **本机在 AI Agent 沙箱里跑要同时满足两个条件**，否则都会表现为 `failed to run bundle_dmg.sh`。
> 实测矩阵（三行都真跑过）：
>
> | 环境 | 结果 | 卡在哪 |
> |---|---|---|
> | 沙箱 + `CI=true` | ❌ | `find_mount_dir()` 里 `hdiutil info \| grep -E --color=never "${dev_name}"`：沙箱把 `grep` 换成代理实现，报出**与正则无关的**「方括号不平衡」，`exit 1` |
> | 干净环境 + 不带 `CI` | ❌ | `osascript` 驱动 Finder 做美化，需要「自动化 / Apple Events」权限，未授予则 `exit 64` |
> | 干净环境 + `CI=true` | ✅ | `Finished 2 bundles` |
>
> 即：`unset BASH_ENV; PATH=/usr/bin:/bin:/usr/sbin:/sbin`（摘掉代理目录）**且**加 `CI=true`
> （Tauri 见到它才给 `bundle_dmg.sh` 传 `--skip-jenkins`）。**两者都是环境问题，不是项目问题。**
>
> - 打包失败时 `.app` 通常已经生成好了（`Bundling NexTerm.app` 排在 dmg 之前），不必卡在 dmg 上。
> - CI 上不需要额外处理：GitHub Actions 默认就设了 `CI=true`，且没有 `grep` 代理。

### 6.2 macOS 签名：**不配 `signingIdentity` 等于不签名**（踩过，很隐蔽）

**这一节原来写错了**，原文说「产出的都是 ad-hoc 签名」。实测并非如此 ——
Tauri **默认根本不跑 `codesign`**，`bundle.macOS.signingIdentity` 为空时直接跳过。
后果是 app 里只剩 **链接器自动打的签名**，而它不是合法的 bundle 签名。

先配好，否则后面全是坑（`tauri.macos.conf.json`）：

```json
{ "bundle": { "macOS": { "signingIdentity": "-" } } }
```

`-` 是 ad-hoc 的伪标识（官方文档：<https://v2.tauri.app/distribute/sign/macos/#ad-hoc-signing>）。
配了之后构建日志会多出两行 `Signing with identity "-"`（先签主二进制，再签整个 bundle）。

#### 怎么判断到底签没签：看 `flags`

```bash
codesign -dv path/to/NexTerm.app 2>&1 | grep -E '^flags|^Identifier|^Sealed'
codesign --verify --deep --strict --verbose=2 path/to/NexTerm.app; echo "exit=$?"
```

| | 没配 `signingIdentity`（错） | 配了 `"-"`（对） |
|---|---|---|
| `flags` | `0x20002(adhoc,**linker-signed**)` | `0x10002(adhoc,runtime)` |
| `Identifier` | 随机串（如 `nexterm-2f7aa5e9ff7431bd`） | Info.plist 里的 `com.nexterm.desktop` |
| `Sealed Resources` | `none` | `version=2 rules=13 files=1` |
| `Contents/_CodeSignature/` | **不存在** | 有 `CodeResources` |
| `codesign --verify` | ❌ `code has no resources but signature indicates they must be present` | ✅ `valid on disk` / `satisfies its Designated Requirement` |

`linker-signed` 这个 flag 是关键指纹 —— 看到它就知道 codesign 没跑过。

#### 后果：用户看到的是「已损坏」，不是「无法验证开发者」

从浏览器下载的 dmg 会带 `com.apple.quarantine`，拷出 app 时这个标记会跟过去
（dmg 上是 `0281;…`，拷到 `/Applications` 后多出 `0x0100` 位变成 `0381;…`，
该位表示「由应用下载」）。**隔离标记 + 无效签名 = Gatekeeper 判定为「已损坏」**，
是死路：右键「打开」也救不回来。

官方文档对这个症状的原话：

> Code signing is required on macOS … and to prevent a warning that your application is
> **broken and can not be started**, when downloaded from the browser.

修好签名之后，报错会退化成常规的「未验证开发者」，用户可以走
**系统设置 → 隐私与安全性 → 「仍要打开」**（或右键 →「打开」）放行一次。

> **注意**：ad-hoc 签名**不能**完全免掉这一步。官方原文：
> *Ad-hoc code signing does not prevent MacOS from requiring users to whitelist the
> installation in their Privacy & Security settings.*
> 要彻底无提示，需要 Apple Developer 证书（Developer ID Application）+ 公证（notarize），
> 目前没有。CI 里若配了 `APPLE_CERTIFICATE` / `APPLE_SIGNING_IDENTITY` 等 secrets ，
> `tauri-action` 会自动接手，无需改配置。

#### 应急修法（用户手上已经有坏包时）

```bash
# 1) 去掉隔离标记（签名坏了时，这一条就足以让它能跑）
xattr -dr com.apple.quarantine /Applications/NexTerm.app
# 2) 顺手把签名补正（让 codesign --verify 也能过）
codesign --force --sign - --identifier com.nexterm.desktop /Applications/NexTerm.app
```

> 本机（AI agent 终端）**无法复现** Finder 双击那条 Gatekeeper 评估路径 ——
> 手工 `xattr -w` 注入隔离标记再 `open`，Gatekeeper 并不拦。
> 所以这条只能靠「签名是否有效」这类客观判据来验，别宣称"已复现用户的报错"。


### 6.3 推送 workflow 文件需要 `workflow` scope（踩过一次）

`.github/workflows/*` 是**受保护路径**：用 OAuth App token 推送会**被 GitHub 拒收整个 ref**，
不是只跳过那几个文件。`master` 和 tag 都一样：

```
! [remote rejected] master -> master (refusing to allow an OAuth App to
  create or update workflow `.github/workflows/ci.yml` without `workflow` scope)
! [remote rejected] v0.1.2 -> v0.1.2 (同上)
```

关键区别，**别搞混**：

| 命令 | 行为 | 结果 |
|---|---|---|
| `gh auth login` | 重开一份授权 | scope 回到默认的 `repo` / `read:org` / `gist` —— **没有 `workflow`** |
| `gh auth refresh -s workflow` | 在现有 token 上**补** scope | 变成 `repo, read:org, gist, workflow`，原授权不丢 |

查当前 token 的真实 scope（本地缓存可能骗你，直接问 API 最准）：

```bash
gh api -i user | grep -i '^x-oauth-scopes:'
# → X-Oauth-Scopes: gist, read:org, repo, workflow
```

`gh auth refresh` 走 OAuth device flow，需要人参与（**不能用非交互 shell 代跑**）：

```
! First copy your one-time code: XXXX-XXXX
Open this URL to continue in your web browser: https://github.com/login/device
✓ Authentication complete.
```

> 如果 CI 因为这条挂掉，症状是 push 直接被拒（本地 `git log` 与远端不一致），
> 而不是 Actions 页面报红 —— 容易误判成「代码有问题」。先查 scope。

---

### 6.4 macOS 上别留 `.app` 副本，否则 Spotlight 里冒出多个「NexTerm」（踩过一次）

**症状**：`⌘Space` 搜应用名，出来好几个一模一样的 NexTerm 图标。

**成因**：macOS 的 Spotlight 是**按 `CFBundleIdentifier` 归类应用**的，不看路径。
只要有第二个 `.app` 带着同样的 `com.nexterm.desktop`，它就会被当成「另一个已安装的 NexTerm」。
2026-09-29 实际踩到 5 个副本：

| 来源 | 路径 | 说明 |
|---|---|---|
| 正式版 | `/Applications/NexTerm.app` | ✅ 唯一该留的 |
| 装机回滚备份 ×3 | `~/Library/Application Support/NexTerm/backup/NexTerm-<ver>-<ts>[-prefix\|-brokensig].app` | 装一次存一个、从不清理 |
| 构建产物 | `<repo>/target/release/bundle/macos/NexTerm.app` | `tauri build` 的默认产物路径 |

> 顺手认号：那个 `-brokensig` 备份的签名是 `flags=0x2(adhoc)`（**无 `runtime`**），
> 正是当初报「已损坏」的那一版 —— `codesign -dv <app> | grep flags` 一眼能认出来。

#### 唯一有效的修法：**别让第二个 `.app` 存在**

1. **装机备份不要用 `.app` 后缀** —— 改成 `NexTerm-<ver>-<ts>.app.bak`，或者直接压成
   `.zip` / `.tar.gz`。后缀一变，Spotlight 就不把它当应用了。
   备份目录 `~/Library/Application Support/NexTerm/backup/` 也要**只保留最近一个**。
2. `<repo>/target/release/bundle/macos/NexTerm.app` 是 Tauri 的固定产物路径，删不得（出 dmg 要用）。
   想让它不出现在搜索结果里，只有一条路：**系统设置 → 聚焦 → 搜索结果 → 隐私列表里加入
   `<repo>/target`**（GUI 操作，需要 root/用户本人）。脚本无法代劳。

#### ❌ 实测否证：目录级标记文件在这台机器上**没用**

网上常见的 `.metadata_never_index` / `.noindex` 说法，在本机（macOS 26）**经对照实验证伪**：

```bash
# 四个目录，各放不同标记，外加一个纯对照组；mdimport 强制导入后按文件名查
mkdir -p $BASE/{ctrl,meta_never_index,noindex,subdir_marker}
echo x > $BASE/ctrl/UNIQ_CTRL_1234.txt                  # 对照
echo x > $BASE/meta_never_index/UNIQ_MNI_1234.txt
echo x > $BASE/noindex/UNIQ_NOI_1234.txt
touch $BASE/meta_never_index/.metadata_never_index      # 候选 1
touch $BASE/noindex/.noindex                            # 候选 2
mdimport -i $BASE && mdfind -name 'UNIQ_'                # 结果见下
```

| 目录 | 标记 | 是否仍被索引 |
|---|---|---|
| `ctrl` | 无（对照组） | ✅ 被索引 |
| `meta_never_index` | `.metadata_never_index` | ⚠️ **仍被索引**（标记无效） |
| `noindex` | `.noindex` | ⚠️ **仍被索引**（标记无效） |
| `subdir_marker` | `.metadata_never_index_subdirectories` | ⚠️ **仍被索引**（标记无效） |

对照组与三个实验组结果**完全一致** —— 说明这些标记根本没被 mds 识别。
**不要再往仓库里塞这类标记文件**，也不要据此改构建脚本（曾一度这么改过，已回滚）。

> 实验设计教训：第一次把探针放在 `$TMPDIR`（`/var/folders/…`）里跑，
> 结果是「实验组和对照组都是 0 条」—— 那是**假阴性**，因为 macOS 本来就**不索引**
> `/var/folders` 这个私有临时目录。**对照组必须放在确定会被索引的位置**（家目录 / 仓库），
> 否则「两组都没命中」会被误读成「标记有效」。

#### 清完之后自查（按 bundle id 查最准，`mdfind -name` 会漏）

```bash
mdfind 'kMDItemCFBundleIdentifier == "com.nexterm.desktop"'
# 期望只剩 /Applications/NexTerm.app（+ 未加入隐私列表的 target/ 产物）
```

> `/Volumes/*` 不是多图标的原因：挂载的 dmg 默认就不参与索引
> （`mdutil -s /Volumes/xxx` 会报 `Indexing disabled`）。但**残留挂载该弹还是要弹** ——
> 尤其构建中途失败时留下的 `rw.<pid>.NexTerm_<ver>_<arch>.dmg` 临时镜像，
> 源文件可能已被删、挂载点却还在，要用 `hdiutil detach -force` 清掉。

---

## 7. 本机 shell 的环境：`LANG` 与 `TERM` 得由我们注入（踩过一次）

### 症状

macOS 上打开本地终端，**中文文件名整片变成问号**：`中文目录Ω` → `??????????????`（14 个字节 = 14 个问号）。

### 排查时最容易被带偏的地方

`printf` / `cat` / `echo` 的中文**完全正常**，坏的只有 `ls`、`find` 这类命令 ——
于是很容易去怀疑渲染层（xterm.js 字体缺 CJK 字形 / 编码转换 / IPC 通道），**全是错的方向**。
链路本身没问题：PTY 出来的是原始字节，`transcoder.rs` 按 UTF-8 原样透传，`Channel<Vec<u8>>` 一个字节不丢。

真正的判别点有两条，缺一不可：

1. **程序是否按 locale 判断字符可打印性** —— `printf` 不做这件事，`ls` 做；
2. **stdout 是不是 tty** —— BSD `ls` 的 `-q`（非可打印字符 → `?`）**只在 stdout 是 tty 时默认开启**。
   所以 `ls | cat` 和 `$(ls)` 都能正常显示中文，**唯独用户在终端里直接敲的 `ls` 会变问号** ——
   这一条会让「用脚本复现」的做法直接得出错误结论。

### 根因

macOS 的 GUI 应用由 launchd 启动，环境里**没有 `LANG` / `LC_ALL`**。NexTerm 实测（`ps eww <pid>`）：

```
PATH=/usr/bin:/bin:/usr/sbin:/sbin   SHELL=/bin/zsh   HOME=…   USER=…   TMPDIR=…
# 没有 LANG、没有 LC_ALL、也没有 TERM
```

从这里 fork 出的 shell 落在 C locale（`locale charmap` = `US-ASCII`）。macOS 其实自己备了解药，
`/etc/zprofile` 里写着 `if [ -z "$LANG" ]; then export LANG=C.UTF-8; fi`
—— **但那是登录 shell 才读的文件**。Terminal.app / iTerm 默认起登录 shell（`zsh -l`），所以它们没这个问题；
本应用起的是普通交互 shell，读不到它。

实测对照（同一 GUI 环境、同一夹具、`ls -1` 直出 tty）：

| 启动方式 | `locale charmap` | `ls` 中文名 |
|---|---|---|
| `zsh`（本应用原状） | `US-ASCII` | ❌ `????????????` |
| `zsh -l`（＝Terminal.app） | `UTF-8` | ✅ |
| `zsh` + `LANG=C.UTF-8` | `UTF-8` | ✅ |
| `zsh` + 只加 `TERM=xterm-256color` | `US-ASCII` | ❌（这条对照组说明与 TERM 无关） |

### 修法：注入环境，而不是改用户的 shell

`transport/local.rs` 在起子进程时注入（不去改成登录 shell —— 那会连带 source
`~/.zprofile` / `~/.zlogin`，副作用比一个环境变量大得多）：

- **`LANG=C.UTF-8`，只在宿主三个变量都为空时才填。** 宿主已设 locale 时一律透传，尊重用户的选择
  （从终端里 `pnpm tauri dev` 起来就属于这种）。选 `C.UTF-8` 而不是 `zh_CN.UTF-8`：它正是 macOS
  给登录 shell 的值（`locale -a` 在册），语言仍是 C —— 错误信息保持英文，便于按文本解析命令输出的
  调用方；变的只是 charmap。空串按「没设」处理（有启动器会塞 `LANG=`）。
- **`TERM=xterm-256color`，无条件覆盖。** TERM 描述的是**我们提供的这个 pty**（xterm.js，256 色），
  不是启动 NexTerm 的那个终端；宿主若在 tmux 里（`TERM=screen`），透传下去会让子进程按错误的能力表
  发序列。原先只在 Windows 分支设，Unix 上一直是「根本没有 TERM」（`vim` / `less` 退化成哑终端、
  `ls` 不上色）。
- **`exec`（非 PTY）只补 locale、不设 TERM** —— 那里没有终端，TERM 没有意义。

### 自查

```bash
# 1. GUI 应用的宿主环境到底有没有 locale
ps eww $(pgrep -f 'NexTerm.app/Contents/MacOS') | tr ' ' '\n' | grep -E '^(LANG|LC_|TERM)='
# 2. 在终端里问 shell 自己（US-ASCII 就是没生效）
locale charmap
# 3. 回归测试：走应用自己的 open_pty 路径，只在宿主无 locale 时判得动（--nocapture 看跳过原因）
cargo test --test local_pty -- --nocapture
```

---

## 8. 已知缺口（未处理，按优先级）

| P | 缺口 | 说明 | 建议 |
|---|---|---|---|
| ✅ | ~~pnpm 版本口径三处不一致~~ | 已统一：`ci.yml` / `pages.yml` / `release.yml` 一律钉 11，README 从「pnpm 9+」改为「pnpm 11+」 | — |
| ✅ | ~~CI 不构建安装包~~ | 已补 `release.yml`：tag 触发的 `tauri-action` 矩阵，macOS 出 dmg、Windows 出 NSIS | — |
| P2 | 只有 arm64 安装包 | `macos-latest` 是 Apple Silicon，Intel Mac 装不了 | 要通用包得 `--target universal-apple-darwin`（装两个 rust target，构建时间翻倍）；有 Intel 用户再加 |
| P2 | Linux 未进矩阵 | `dpapi.rs` 有 Linux 桩、`local.rs` 有 `#[cfg(not(windows))]` 分支，但没人验 | 要么声明「不支持 Linux」，要么把 `ubuntu-latest` 加进矩阵 |
| P3 | `transport::ExecResult` 的裁剪职责靠约定 | 裁剪已从传输层挪到「喂模型的那一侧」，靠注释和回归用例守；漏调 `cap_text` 不会编译报错 | 若想强制，用类型区分 `RawOutput` / `CappedOutput` |
| P3 | `MountPanel` 默认挂载点是 `Z:` | Windows 盘符，Linux 上不合理；macOS 已标不可用所以暴露不出来 | 按平台给默认值（Windows `Z:` / 其余 `/mnt/point`） |
| ✅ | ~~Windows 分支的 PowerShell 语法未经本地实测~~ | 已由 CI 实测关闭：`windows-latest` 跑出 `local_exec_keeps_full_output ... ok`（run `36532246405`，Windows 171 passed） | — |
| ✅ | ~~macOS 包签名~~ | 已修：原来**根本没签名**（`signingIdentity` 未配 ⇒ Tauri 跳过 codesign，只剩 linker 签名），用户下载后报「已损坏」。现已配 `"signingIdentity": "-"`，`codesign --verify` 通过 | — |
| P2 | macOS 包未公证（notarize） | 现为有效的 ad-hoc 签名，不再报「已损坏」，但用户首次打开仍需在「隐私与安全性」里放行一次 | 需 Apple Developer 证书（$99/年）：CI 配 `APPLE_CERTIFICATE` / `APPLE_ID` / `APPLE_PASSWORD` 等 secrets 后 `tauri-action` 自动接手 |
| P3 | `pnpm-workspace.yaml` 同时留着两代字段 | `onlyBuiltDependencies`（pnpm 10/11 读）与 `allowBuilds`（pnpm 12 读）并存。**这是有意为之**，让跨大版本都能装上依赖；副作用是读起来像笔误 | 若彻底钉死 11，可删掉 `allowBuilds` 两行 |
