<div align="center">

# NexTerm

**SSH、WinRM、文件、Docker、数据库和 AI，集中在一个工作台。**

[![Website](https://img.shields.io/badge/%E5%AE%98%E7%BD%91-online-516cd6)](https://nexterm.work/)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8)
![Wails](https://img.shields.io/badge/Wails-v3-CC0000)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

</div>

## 功能

- **终端与文件**：SSH、WinRM 和本机终端，支持多标签、分屏、SFTP 文件管理与在线编辑。SSH 终端是守护终端：关闭标签或断开后转入「后台会话」继续运行，随时可接管；本机、WinRM 非交互与容器 exec 等普通终端随关闭结束。
- **终端历史**：每条终端会话自动录制（输出、键盘输入与窗口尺寸变化），可按文本查看、搜索或按时间轴回放。注意键盘输入会原样留在记录中，在终端里输入的密码、令牌等敏感内容也会被记录；记录默认只存本机，可对单条开启端到端加密同步，也可逐条删除。
- **日常运维**：管理 Docker 容器与镜像，使用 PostgreSQL、MySQL、Redis 和 SSH 端口转发；PostgreSQL 与普通 SSH/SFTP 均不需要远端 agent，agent 仅用于设备管理。
- **AI 助手**：接入 OpenAI 兼容接口；命令输出和文件变更可见，敏感操作需要确认；支持终端接管与定时任务。
- **多用户账号**：服务端内置账号体系，超管初始化后可创建用户或开放注册；同步数据按账号隔离并端到端加密。
- **设备管理与分享**：主机安装设备 agent 后纳入设备列表，可远程打开设备终端；主机可分享给其他注册用户，或生成公开链接（默认只读），全程不暴露主机密码与私钥。
- **桌面与浏览器**：桌面端适合本机使用；服务端的终端进程和工作区保存在服务器上，关闭网页后可继续。
- **资产同步**：桌面端登录账号后，资产、分组、片段和凭据在设备之间端到端加密同步，不必重复配置。

## 下载与安装

从 [Releases](https://github.com/Hello-CTF/NexTerm/releases/latest) 下载最新版本，或在懒猫微服的应用中心安装。

| 平台 | 安装包 |
| --- | --- |
| Windows 10/11（x64 / ARM64） | `NexTerm_x.y.z_<架构>-setup.exe`，运行安装器。 |
| macOS（Apple Silicon / Intel） | `NexTerm_x.y.z_<架构>.dmg`，打开后拖入「应用程序」。 |
| Linux 桌面（amd64 / arm64） | `NexTerm-desktop_x.y.z_linux_<架构>.deb`，`apt install ./<包名>.deb` 安装（自动装 GTK4 与 WebKitGTK 6.0 依赖）。 |
| Linux 服务器 | `NexTerm-server_x.y.z_linux_<架构>.tar.gz`，提供完整浏览器界面，安装见包内 README。 |
| 懒猫微服 | 在应用中心安装 NexTerm。 |

macOS 首次打开提示无法验证开发者时，在「系统设置 → 隐私与安全性」中点「仍要打开」；提示应用已损坏则在终端执行 `xattr -dr com.apple.quarantine /Applications/NexTerm.app`。

首次使用：

1. 添加 SSH 或 WinRM 资产，也可以直接使用内置的「当前设备」。
2. 保存密码或私钥前，先按界面提示在「设置 → 凭据保护」初始化凭据库；未初始化时无法保存任何凭据。免密保护依赖系统级密钥（Windows DPAPI / macOS 钥匙串），Linux 桌面端不提供，请改用主密码模式：勾选"用密码保护凭据"并设置至少 8 位的保护密码，每次启动需输入密码解锁，闲置自动锁定。
3. 需要使用 AI 时，在设置中填写 OpenAI 兼容接口地址、API Key 和模型名称。
4. 桌面端不登录账号即可本地使用；在「设置 → 账号同步」登录后，资产可在多台设备之间同步。

### 自动更新

桌面版启动后静默检查一次更新，不弹通知；发现新版本时窗口顶部显示更新横幅，可在「设置 → 软件更新」中查看版本号、安装包大小与更新说明，一键下载安装。更新通道跟随当前版本：预发布版收到预发布更新，正式版只收到正式版更新。安装前自动按 Release 附带的 SHA256SUMS 校验。服务端不自动更新，请按部署包内的说明升级。

## 服务端与设备接入

- **LinuxServer**：解压 server 包后，按包内 `README.md` 安装二进制、systemd 服务与环境文件。首次启动时服务端在控制台打印一次性初始化码（systemd 下用 `journalctl -u nexterm-server` 查看），在浏览器打开界面，输入初始化码并设置超管用户名与密码。访问控制默认开启（`--auth on`），无论监听地址都要求先登录账号；公网部署请只监听回环地址，并在前面配置带 TLS 的反向代理。完整命令行选项见 `nexterm-server --help`。
- **懒猫微服**：应用中心安装后直接打开使用，终端进程和数据保存在你的微服上。微服不提供 NexTerm 内置端口转发，需要对外提供远端端口时请用微服平台自带的转发功能。
- **设备接入**：在服务端「设备管理」签发一次性接入码，在设备上执行 `nexterm-server agent enroll --server <接入地址> --code <接入码>` 兑换设备凭证，再执行 `nexterm-server agent install` 装成随登录自启的服务；全新 Linux 机器可用面板里的一行安装命令一次完成。设备可随时远程打开终端，也可吊销（凭证立即失效，不可恢复）。设备管理只在浏览器模式下可用。

## 从源码构建与测试

工具链：Go 1.26、Node 24+ 与 pnpm。

```bash
pnpm install     # 安装前端依赖
pnpm build       # 类型检查并构建前端产物到 dist/
go build ./...   # 编译全部 Go 包
```

常规本地质量门禁，装好 task 后一次跑完：

```bash
task check   # gofmt + go vet + go test ./... + go mod verify + pnpm typecheck + pnpm lint + pnpm test + bindings 校验 + 前端产物可复现校验 + LazyCat manifest injects 校验
```

workflow 复用接线检查与 PostgreSQL 真实测试使用单独入口：

```bash
task verify:wiring
NEXTERM_TEST_PG_DSN='<dsn>' task test:pg
```

PostgreSQL 门控测试直接经 Go 运行时，未设置 `NEXTERM_TEST_PG_DSN` 会跳过；`task test:pg` 在 DSN 缺失时直接失败，不把 SKIP 当作通过。桌面端与服务端安装包的完整打包见 `node scripts/build.mjs --help`。

## License

[MIT](LICENSE)
