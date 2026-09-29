# NexTerm 跨平台规范（Windows / macOS）

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

### 6.2 macOS 包的签名现状

本机与 CI 产出的都是 **ad-hoc 签名、未公证**（`codesign -dv` 显示 `Signature=adhoc`、
`Info.plist=not bound`、`TeamIdentifier=not set`）。因此用户首次打开可能被 Gatekeeper 拦下，
需要右键「打开」或 `xattr -dr com.apple.quarantine /Applications/NexTerm.app`。
要免掉这一步，得有 Apple Developer 证书 + `notarize` 配置，目前没有。

---

## 7. 已知缺口（未处理，按优先级）

| P | 缺口 | 说明 | 建议 |
|---|---|---|---|
| ✅ | ~~pnpm 版本口径三处不一致~~ | 已统一：`ci.yml` / `pages.yml` / `release.yml` 一律钉 11，README 从「pnpm 9+」改为「pnpm 11+」 | — |
| ✅ | ~~CI 不构建安装包~~ | 已补 `release.yml`：tag 触发的 `tauri-action` 矩阵，macOS 出 dmg、Windows 出 NSIS | — |
| P2 | 只有 arm64 安装包 | `macos-latest` 是 Apple Silicon，Intel Mac 装不了 | 要通用包得 `--target universal-apple-darwin`（装两个 rust target，构建时间翻倍）；有 Intel 用户再加 |
| P2 | Linux 未进矩阵 | `dpapi.rs` 有 Linux 桩、`local.rs` 有 `#[cfg(not(windows))]` 分支，但没人验 | 要么声明「不支持 Linux」，要么把 `ubuntu-latest` 加进矩阵 |
| P3 | `transport::ExecResult` 的裁剪职责靠约定 | 裁剪已从传输层挪到「喂模型的那一侧」，靠注释和回归用例守；漏调 `cap_text` 不会编译报错 | 若想强制，用类型区分 `RawOutput` / `CappedOutput` |
| P3 | `MountPanel` 默认挂载点是 `Z:` | Windows 盘符，Linux 上不合理；macOS 已标不可用所以暴露不出来 | 按平台给默认值（Windows `Z:` / 其余 `/mnt/point`） |
| P3 | Windows 分支的 PowerShell 语法未经本地实测 | `transport/mod.rs` 的 `1..1500`：本机（macOS）无 `pwsh`，只能由 CI 的 windows 任务验证 | 已有注释标注；首次 Windows CI 跑绿后即可去掉这行提醒 |
| P3 | macOS 包未签名 / 未公证 | ad-hoc 签名，用户首次打开要绕 Gatekeeper | 需要 Apple Developer 证书 |
| P3 | `pnpm-workspace.yaml` 同时留着两代字段 | `onlyBuiltDependencies`（pnpm 10/11 读）与 `allowBuilds`（pnpm 12 读）并存。**这是有意为之**，让跨大版本都能装上依赖；副作用是读起来像笔误 | 若彻底钉死 11，可删掉 `allowBuilds` 两行 |
