<div align="center">

<img src="website/public/brand/nexterm-mark-128.png" width="88" alt="NexTerm" />

# NexTerm

**一个可自托管的工作台，管好每一台服务器。**

可接管的持久终端 · 可执行操作的 AI 助手 · 文件、Docker、数据库、设备管理与终端分享

[![Website](https://img.shields.io/badge/%E5%AE%98%E7%BD%91-nexterm.work-516cd6)](https://nexterm.work/)
[![Release](https://img.shields.io/github/v/release/Hello-CTF/NexTerm?color=516cd6)](https://github.com/Hello-CTF/NexTerm/releases/latest)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8)
![Wails](https://img.shields.io/badge/Wails-v3-CC0000)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

**中文** | [English](README_EN.md)

[官网](https://nexterm.work/) · [下载](https://github.com/Hello-CTF/NexTerm/releases/latest) · [部署服务端](#部署服务端)

</div>

<picture>
  <source media="(prefers-color-scheme: light)" srcset="website/public/images/shell-light.jpg" />
  <img src="website/public/images/shell-dark.jpg" alt="NexTerm 的 Shell 终端与命令块界面" />
</picture>

NexTerm 把远程运维放进一个窗口：通过 SSH、WinRM 或设备代理连接主机，让 AI 助手在你确认后排查和修复问题，再处理文件、容器、数据库、设备监控和终端分享。图片使用示例数据，可在[官网](https://nexterm.work/)体验在线演示。

## 功能

- **🤖 可执行操作的 AI**：20+ 工具覆盖命令、文件、Docker 和终端输出；执行前展示命令或 diff，支持计划模式、按主机和路径授权、定时任务和长期记忆；兼容任意 OpenAI 接口，内置 DeepSeek、Kimi、GLM、Ollama 预设。
- **🖥️ 持久终端**：桌面端的本机终端默认运行在独立的本机持久组件中，SSH 会话和受管设备会话在启用持久终端后也一样：关闭标签、退出 App 或断网后继续运行，可在「后台会话」接管或终止，也可从任意已登录设备接管并补回输出（主机端每个持久终端保留最近约 8 MB 输出，超出时丢弃最早的部分；断线重连从断点补齐，重新打开只先显示最近一段）。标签上的持久图标和可关闭的提示条说明终端是否持久；组件准备中或无法启用时新建的是临时终端并标明原因，临时终端断开即结束。从资产树打开主机只打开它的工作区并在后台准备组件，不自动新建终端（快速连接 user@host 仍立即打开终端）。设置里的「默认终端模式」选临时终端时不再准备组件，资产单独设置优先；移动端没有本机终端。升级替换旧组件会结束其中的会话，替换前会列出数量并要求确认。
- **🔔 先看哪个会话需要处理**：标签和工作区上的徽章显示会话状态（运行中、需要处理、已结束）；顶栏铃铛的收件箱列出所有需要你的会话，包括你在本设备没有打开标签的主机；桌面端可弹出系统通知（可分项关闭，多台设备时最近活动的设备先通知，其余 60 秒后补发）。「需要处理」来自终端响铃、程序通知（OSC 9/777/99）和你自己保存的规则，NexTerm 不会从屏幕内容猜测程序是否在等你，也不提供通过按钮回答提问的功能，提问仍需在终端中回答。移动端首页是状态墙，按需要处理、运行中、已结束分组。在标签上选「这需要处理」或「状态不对」即可保存规则，可让 AI 根据屏幕起草（发送给模型前会尽力脱敏疑似密钥，不保证全部识别，保存前请核对），规则在「设置」里编辑、测试和排序，并应用到持久终端组件。
- **📱 多设备**：同一终端可被多台设备同时观看和输入，没有「接管」步骤；终端尺寸跟随最后输入的设备（短暂保持），其他设备以缩放后的画面显示；实时 OSC 52 剪贴板只写入最后输入的设备。前提：手机只在桌面端 App 运行时（托盘常驻也算）才能看到该桌面的本机终端，需在「设置」中开启「向服务器账号开放本机终端」；只有 SSH 的主机，手机只能通过它自己的 SSH 会话或已接入的设备代理看到。
- **🧭 导航与历史**：跳转面板（Ctrl/Cmd+Shift+P）按需要程度排序会话并显示状态徽章，Alt+数字直接打开，Shift+Enter 拆到右侧，也可「重连当前工作区」；工作区顶部有重连条。窗格是可嵌套的分屏树，关闭窗格会把其中标签并入相邻窗格（Ctrl/Cmd+Shift+W，网页版 Ctrl+Alt+W）；终端内搜索在非 macOS 上为 Ctrl+Shift+F（Ctrl+F 交给 shell）。本机滚动行数默认 10000 行（「设置 → 终端」），持久终端滚到顶部可「加载更早历史」只读查看。SSH 连接用保活和入站流量判断存活，仅在硬错误或持续无响应时判定断开，连接存疑时也可手动重连。
- **🔄 同步**：状态规则、Runbook 模板、AI 技能默认随账号同步，AI 记忆和 AI 定时任务需在「设置 → 账号同步」单独开启（定时任务只在绑定的设备上运行）。开放本机终端等于把这台电脑当前用户的命令行权限交给该服务器账号：服务器的管理员和任何能控制该服务器或账号的人，包括通过该账号的 AI 助手、定时任务和批量执行，都能以当前用户身份执行任意命令，请只对你信任的服务器开启。支持多台设备同看、命令块、历史搜索与回放、命令广播和片段。
- **🧰 日常运维**：SFTP/WinRM/设备文件浏览与在线编辑；Docker 容器、日志与镜像；MySQL/MariaDB、PostgreSQL 与 Redis；本地转发与 SOCKS5；从 `~/.ssh/config` 或 Termius（仅 macOS）导入主机，支持单跳 `ProxyJump`，多跳条目需手动添加。
- **🛰️ 设备与分享**：一行命令接入设备代理，设备主动外连并上报指标；终端可分享为限时公开链接或分享给注册用户（仅终端，不含文件、转发和 AI），默认只读、可随时吊销，无需交出密码或私钥；已接入的设备代理可用 `nexterm-agent update` 升级；浏览器模式可授权写入，桌面端创建的公开链接只读。
- **🔐 自托管**：多用户与 TOTP 双因素认证；端到端加密同步；凭据保险库（主密码、DPAPI/钥匙串或生物识别）；审计日志；数据库可选 SQLite、PostgreSQL、MySQL、MariaDB。浏览器模式下终端和工作区保存在服务端，关闭网页后仍可继续。

<table>
<tr>
<th width="33%">AI 助手</th>
<th width="33%">远程文件</th>
<th width="33%">终端回放</th>
</tr>
<tr>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/ai-light.jpg" /><img src="website/public/images/ai-dark.jpg" alt="AI 助手对话与命令确认" /></picture></td>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/files-light.jpg" /><img src="website/public/images/files-dark.jpg" alt="SFTP 文件树与远程编辑" /></picture></td>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/replay-light.jpg" /><img src="website/public/images/replay-dark.jpg" alt="终端历史时间轴回放" /></picture></td>
</tr>
</table>

## 下载

从 [Releases](https://github.com/Hello-CTF/NexTerm/releases/latest) 下载：

| 平台 | 安装包 |
| --- | --- |
| Windows 10/11（amd64 / arm64） | `nexterm-desktop_windows_<架构>_x.y.z.exe` |
| macOS（arm64 / amd64） | `nexterm-desktop_macos_<架构>_x.y.z.dmg` |
| Linux 桌面（amd64 / arm64） | `nexterm-desktop_linux_<架构>_x.y.z.deb`，用 `apt install ./<包名>.deb` 安装；需要 GTK 4.14+ 与 WebKitGTK 6.0；`.deb` 按构建出的二进制声明所需的最低 glibc，系统不满足时 apt 会拒绝安装 |
| Linux 服务器 | 见[部署服务端](#部署服务端) |
| Android（预览，arm64） | `nexterm-desktop_android_arm64_x.y.z.apk` |
| iOS / iPadOS（预览） | [加入 TestFlight 测试](https://testflight.apple.com/join/ds62pzPz)，Release 不提供 IPA |
| 懒猫微服（amd64） | 应用中心 |

移动端为预览版，完成真机签名验收前不作为正式支持平台。桌面版会自动检查更新，安装前按 `SHA256SUMS` 校验。Windows 安装包没有代码签名，提示未知发布者时选择「更多信息 → 仍要运行」。macOS 提示无法验证开发者时，在「系统设置 → 隐私与安全性」中选择「仍要打开」；提示已损坏则执行 `xattr -dr com.apple.quarantine /Applications/NexTerm.app`。Linux 桌面版的彩色 emoji 依赖系统字体，`.deb` 推荐安装 `fonts-noto-color-emoji`；若安装时跳过了推荐包，标签标题中的 emoji 可能显示为单色符号或方框，可执行 `sudo apt install fonts-noto-color-emoji` 补装。

**快速开始**：新建 SSH 或 WinRM 资产（也可用内置的「当前设备」）→ 在「设置 → 凭据保护」初始化凭据库后保存密码或私钥 →（可选）在「设置 → AI 模型」接入模型 →（可选）在「设置 → 账号同步」登录服务端，多设备同步。保护密码只用于本机解锁，忘记后已保存的凭据无法恢复。

**AI 信息脱敏**：默认关闭，在「设置 → 信息脱敏」下载最新发布的 `QuantumNous/astrlink-guard` 模型，选择 INT8、FP32 或 FP16 后启用。模型权重不随应用分发。模型从 Hugging Face 下载。本地推理运行库目前要求 Linux（可选的 ONNX Runtime 库需要 glibc 2.27 或更新并带 libstdc++，不支持 musl）、Windows（安装包已随附 VC++ 运行库）、macOS 13.4+、iOS/iPadOS 15.1+ 或 Android 7.0+；ONNX Runtime 官方不再发布的平台（目前为 Intel Mac）上脱敏不可用，应用其余功能不受影响。未改变应用本身的最低系统版本。检测在运行 AI 的设备执行，浏览器模式在服务端执行；同一会话中的占位符保持不变，回复和工具参数会还原。启用后模型或运行时不可用会阻止请求，不会退回明文发送；图片等非文本内容不会脱敏，会原样发送给 AI 服务，首次在启用状态下添加图片时会提示。识别可能遗漏敏感信息。实现复用 [AstrLink](https://github.com/Calcium-Ion/AstrLink)，来源版本和许可证见 [声明](internal/ai/privacy/third_party/ASTRLINK_NOTICE.md)。

## 部署服务端

在带 systemd 的 Linux（amd64 / arm64，服务端可在任意基于 glibc 的 Linux 上运行（对 glibc 版本无最低要求），不支持 musl（如 Alpine）；可选的本地 AI 脱敏运行库需要 glibc 2.27 或更新并带 libstdc++，不支持 musl）上以 root 运行：

```bash
curl -fsSL https://nexterm.work/install.sh | sudo bash
```

脚本下载最新服务端包，按 `SHA256SUMS` 校验后执行包内的 `install.sh`：创建 `nexterm` 用户和目录，生成 master key，创建超级管理员 `admin` 并在安装输出中打印一次初始密码（请立即记下，首次登录需修改；也可用 `NEXTERM_ADMIN_PASSWORD` 预先指定），然后启动 `nexterm-server`。重复运行即升级（服务会短暂重启），配置和数据保留，撤销上次升级追加 `--rollback`。指定版本用 `curl -fsSL https://nexterm.work/install.sh | sudo bash -s -- --version x.y.z`；也可以下载 `nexterm-server_linux_<架构>_x.y.z.tar.gz` 解压后执行 `sudo ./install.sh`。

- 服务默认只监听 `127.0.0.1:8080` 且要求登录，可在本机执行 `ssh -L 8080:127.0.0.1:8080 用户@服务器` 后打开 `http://localhost:8080`；配置在 `/etc/nexterm/nexterm.env`，修改后执行 `systemctl restart nexterm-server`。
- `--auth off` 会关闭账号、管理与设备路由，任何能连到监听地址的人都能操作服务端，不能用于公网。
- 公网访问请前置带 TLS 的反向代理，代理覆盖设置 `X-Forwarded-Proto`，并把代理来源 IP 段写入 `NEXTERM_TRUSTED_PROXY_CIDRS`。
- 默认使用 SQLite；用 `NEXTERM_DB=postgres|mysql|mariadb` 和 `NEXTERM_DB_DSN` 切换外部数据库，同一数据库只运行一个实例。
- 备份 `/var/lib/nexterm`、`/etc/nexterm/master.key` 和外部数据库。完整说明见包内 `README.md` 和 `nexterm-server --help`。

**懒猫微服**：应用中心安装即可用（目前仅 amd64 机型），内置端口转发不可用，请使用平台转发。

**设备接入**：超级管理员在「设备管理」（浏览器模式，或桌面端登录服务端账号后）配置接入地址并签发一次性接入码，在设备上执行面板给出的一行命令（Linux/macOS 用普通用户，Windows 用 PowerShell），设备代理会随用户登录自动启动。安装时会把当时的 `SHELL` 和 `PATH` 写入服务定义（`nexterm-agent status` 可查看），修改后重新运行安装命令即可刷新；只在 `~/.zshrc` 中定义的 PATH 条目对设备代理进程不可见（设备代理以非交互登录 shell 读取 PATH，请改放到 `~/.zprofile`），而终端标签会读取 `.zshrc`。

## 开发

工具链：Go 1.27、Node 24+、pnpm；Linux 服务端为 `CGO_ENABLED=0` 构建（通过 purego 加载 ONNX Runtime，动态链接 glibc 但无版本要求），可在任意主机上构建；Windows 桌面端同样无需 C 编译器。CI 与发布中的 Linux 构建和测试直接运行在 GitHub 托管的 Ubuntu runner 上（`ubuntu-latest`，arm64 为 `ubuntu-24.04-arm`）；构建 Linux 桌面端需要 GTK 4.14+ 与 WebKitGTK 6.0 开发包（Debian/Ubuntu 上为 `libgtk-4-dev`、`libwebkitgtk-6.0-dev`）。

```bash
pnpm install && pnpm build && go build ./...
task check
```

从最新 `master` 切分支，经 PR 合回；提交和 PR 标题遵循 Conventional Commits。开发、测试和协作约定见 [AGENTS.md](AGENTS.md)，本地数据库环境见 [deploy/dev/README.md](deploy/dev/README.md)。

## License

[MIT](LICENSE)
