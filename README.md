<div align="center">

# NexTerm

**SSH、WinRM、文件、Docker、数据库和 AI，集中在一个工作台。**

[![Website](https://img.shields.io/badge/%E5%AE%98%E7%BD%91-online-516cd6)](https://probiusofficial.github.io/NexTerm/)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8)
![Wails](https://img.shields.io/badge/Wails-v3-CC0000)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

</div>

## 功能

- **终端与文件**：SSH、WinRM 和本机终端，支持多标签、分屏、SFTP 文件管理与在线编辑。SSH 终端是守护终端：关闭标签或断开连接后转入「后台会话」继续运行，随时可接管；本机、WinRM 非交互与容器 exec 等普通终端随关闭结束，不能转入后台。
- **终端历史**：每条终端会话自动录制（输出、键盘输入与窗口尺寸变化），可在「终端历史」里按文本查看、搜索或按时间轴回放。注意：键盘输入会原样进入记录，在终端里输入的密码、令牌等敏感内容也会留在记录中；记录默认只存本机、不参与同步，可对单条记录开启端到端加密同步，也可逐条删除。
- **日常运维**：管理 Docker 容器与镜像，使用 MySQL、Redis 和 SSH 端口转发。
- **AI 助手**：接入 OpenAI 兼容接口；命令输出和文件变更可见，敏感操作需要确认；支持终端接管与定时任务。
- **多用户账号**：服务端内置账号体系，超管初始化后可创建用户或开放注册；同步数据按账号隔离并端到端加密。
- **设备管理与分享**：主机安装设备 agent 后纳入设备列表，可远程打开设备终端；主机可分享给其他注册用户，或生成公开链接（默认只读，可勾选允许读写），全程不暴露主机密码与私钥。
- **桌面与浏览器**：桌面端适合本机使用；服务端的终端进程和工作区保存在服务器上，关闭网页后可继续。
- **资产同步**：桌面端登录账号后，资产、分组、片段和凭据在设备之间端到端加密同步，不必重复配置。

## 下载与安装

从 [Releases](https://github.com/ProbiusOfficial/NexTerm/releases/latest) 下载最新版本，或在懒猫微服的应用中心安装。

| 使用方式 | 安装方法 |
| --- | --- |
| Windows 10/11 x64 | 下载 `NexTerm_x.y.z_x64-setup.exe`，运行安装器。 |
| Windows 10/11 ARM64 | 下载 `NexTerm_x.y.z_arm64-setup.exe`，运行安装器。 |
| macOS（Apple Silicon，arm64） | 下载 `NexTerm_x.y.z_aarch64.dmg`，打开后将 NexTerm 拖入「应用程序」。 |
| macOS（Intel） | 下载 `NexTerm_x.y.z_x86_64.dmg`，同样拖入「应用程序」。 |
| Linux 桌面（amd64 / arm64） | 下载 `NexTerm-desktop_x.y.z_linux_amd64.tar.gz` 或 `NexTerm-desktop_x.y.z_linux_arm64.tar.gz`，解压后运行（自包含二进制，非 AppImage/deb；需系统已安装 GTK4 与 WebKitGTK 6.0 运行库）。 |
| LinuxServer | 下载 `NexTerm-server_x.y.z_linux_amd64.tar.gz` 或 `NexTerm-server_x.y.z_linux_arm64.tar.gz`，提供完整浏览器界面。 |
| 懒猫微服 | 在应用中心安装 NexTerm，无需下载 Release 安装包。 |

首次使用：

1. 添加 SSH 或 WinRM 资产，也可以直接使用内置的「当前设备」。
2. 保存密码或私钥前，先按界面提示在「设置 → 凭据保护」初始化凭据库；未初始化时无法保存任何凭据。
3. 需要使用 AI 时，在设置中填写 OpenAI 兼容接口地址、API Key 和模型名称。
4. 桌面端不登录账号即可本地使用；在「设置 → 账号同步」登录后，资产可在多台设备之间同步。

免密保护依赖系统级密钥（Windows DPAPI / macOS 钥匙串），Linux 桌面端不提供，请使用主密码模式：在「设置 → 凭据保护」勾选"用密码保护凭据"并设置至少 8 位的保护密码；每次启动需输入密码解锁，闲置自动锁定（默认 30 分钟，可在同一卡片调整）。

### 桌面端自动更新

桌面版启动后静默检查一次 GitHub Releases，不弹通知；发现新版本时窗口顶部显示更新横幅，并在「设置 → 软件更新」卡片中展示新版本号、安装包大小与更新说明，可随时「立即检查」「下载并安装」，或忽略该版本。更新通道跟随当前版本：运行预发布版（如 rc）会收到预发布更新，正式版只收到正式版更新。

安装包下载完成后，先按 Release 附带的 SHA256SUMS 校验，校验通过才安装：

- Windows：运行 NSIS 安装器静默安装（`/S`），先把当前可执行文件改名备份，安装失败自动回滚。
- macOS：挂载 dmg，用新 .app 替换 `/Applications` 中的 NexTerm。
- Linux：从 tar.gz 解出新二进制，原子替换当前可执行文件。

安装完成后点「立即重启」即运行新版本。已知限制：

- 检查走 GitHub REST API 匿名调用，受匿名速率限额约束（每个 IP 每小时 60 次）；触发限额时本次检查失败，稍后再试即可，不影响其他功能。
- Windows 与 macOS 的真实安装链路（NSIS 静默安装、dmg 替换 `/Applications`）未在 CI 中覆盖；应用内安装失败时，可到 Releases 页面手动下载安装。
- 服务端不自动安装，请按部署包内的说明升级（懒猫微服在应用中心更新）。

## 服务端与懒猫微服

### LinuxServer

LinuxServer 适合部署在常开的 Linux 机器上。解压后，按照包内 `README.md` 安装二进制、网页文件和所需的 systemd 服务。以 amd64 包为例：

```bash
tar xzf NexTerm-server_x.y.z_linux_amd64.tar.gz
cd NexTerm-server_x.y.z_linux_amd64
less README.md
```

安装包的 systemd 配置默认监听 `127.0.0.1:8080`，并显式声明 `--auth on`（任何监听地址都要求先登录账号）。首次启动时账号系统未初始化，服务端会在控制台打印一次性初始化码（systemd 下用 `journalctl -u nexterm-server` 查看）；在本机浏览器打开 `http://127.0.0.1:8080`，按提示输入初始化码并设置超管用户名与密码，完成后妥善保存恢复密钥。之后也可通过带 TLS 的 HTTPS 反向代理访问。初始化码只用一次；若遗失，停止服务端后删除 setting 表中 `auth.init_code` 一行再重启即重新生成，默认 SQLite 后端执行 `sqlite3 /var/lib/nexterm/data.db "DELETE FROM setting WHERE key='auth.init_code';"`（PostgreSQL 后端对同名表执行等价的 `DELETE`）。

> **不要把完整版服务端直接暴露到公网。** 访问控制默认开启（`--auth on`）：无论监听地址，`/rpc`、`/ws` 与 `/files/blob` 都要求登录会话（`/healthz` 与页面静态资源保持公开），浏览器首次打开会进入初始化或登录页。回环免登录仅在显式指定 `--auth loopback` 且监听回环地址时生效，该模式校验 Host 只允许 `localhost`、`127.0.0.1`、`[::1]`（防 DNS 重绑定），不匹配返回 421；非回环监听时 `loopback` 等同 `on`。对外访问时，仍建议只监听回环地址，并在前面配置带 TLS 的反向代理。

### 仅同步运行

在同一个 `nexterm-server` 上启用 `--sync-only`，即可只提供账号同步：

```bash
nexterm-server --sync-only --listen 127.0.0.1:8080 --data-dir /var/lib/nexterm
```

该模式只开放账号路由（`/auth/*`）、超管路由（`/admin/*`）、`/sync/v2/push`、`/sync/v2/pull`、`/sync/v2/ids`、`/sync/rpc` 和 `/healthz`，不提供浏览器界面、`/rpc` 或终端、文件、容器接口。`/sync/rpc` 是对端同步命令面，只有 `sync_digest`、`sync_export`、`sync_import` 三个命令，要求登录会话与 CSRF 头（完整版不挂此路由，`/rpc` 已含这三命令）；`/healthz` 的 `commands` 字段如实上报当前命令数，sync-only 下为 3。其中 `/auth/status`、`/auth/init`、`/auth/login`、`/auth/register`、`/auth/recovery/reset`、`/auth/devices/enroll` 是公共端点，匿名可用（五个 POST 端点有频率限制，`GET /auth/status` 可匿名查询）；其余账号数据与 `/sync/v2/*`、`/sync/rpc` 要求登录会话，会话内写操作额外要求 CSRF 头，`/admin/*` 还要求超管身份。同步数据按账号隔离并端到端加密，服务端只存密文。账号初始化与完整版相同：未初始化的库首次启动会在控制台打印一次性初始化码；`--sync-only` 没有浏览器界面，可先用完整模式对同一数据目录完成初始化，再切换过来。

在桌面端的「设置 → 账号同步」中填写服务端地址、用户名和密码即可；密码会发送到服务端完成登录认证，同时在本机用于解锁数据密钥；数据密钥本身与同步内容的明文不会离开本机，服务端只存密文。公网地址必须使用 HTTPS（客户端强制校验）；回环、私网与链路本地地址允许 HTTP，但 HTTP 不加密传输中的密码，内网部署同样建议使用 HTTPS（自签证书可勾选跳过证书校验）。

### 懒猫微服

从应用中心安装后直接打开 NexTerm。微服会注入凭据库所需的根密钥，终端进程和数据保存在你的微服上。

懒猫微服不提供 NexTerm 内置端口转发；需要对外提供远端端口时，请使用微服平台自带的转发功能。

## 设备接入（agent）

设备管理只在浏览器模式（连接服务端并登录）下可用，桌面端没有设备列表。接入分三步：

1. 在「设备管理」点「接入新设备」，选择有效期并签发一次性接入码。接入码单次使用、只显示这一次，到期自动作废。超级管理员先在「接入地址」里添加并保存接入地址，否则设备无法接入；配置了多个接入地址时按顺序尝试，第一个可达的即接入点。
2. 在设备上执行 `nexterm-server agent enroll --server <接入地址> --code <接入码>`，把接入码兑换成设备凭证；凭证只写入设备本地数据目录，界面不会再显示任何设备密钥。
3. 执行 `nexterm-server agent install`，把设备 agent 装成随登录自启的 per-user 服务。回到「设备管理」刷新，设备显示「在线」，之后定期上报心跳与系统指标（默认每 60 秒一次）。

全新 Linux 机器（x86_64 / aarch64，无需已装二进制）可用面板里的一行安装命令替代第 2、3 步，自动完成下载、校验、注册与服务安装。设备可随时远程打开设备终端，也可吊销；吊销后设备凭证立即失效、控制通道断开，不可恢复。设备终端的标签关闭即脱离（设备端会话保留），「结束终端」是唯一销毁路径且需显式确认。

## 从源码构建与测试

工具链：Go 1.26、Node（20.19+ 或 22.12+）与 pnpm。

```bash
pnpm install     # 安装前端依赖
pnpm build       # 类型检查并构建前端产物到 dist/
go build ./...   # 编译全部 Go 包
```

质量门禁与 CI 一致，装好 task 后可一次跑完：

```bash
task check   # gofmt + go vet + go test ./... + go mod verify + pnpm typecheck + pnpm lint + pnpm test + bindings 校验 + 前端产物可复现校验
```

也可分别执行 `go test ./...`、`pnpm typecheck`、`pnpm lint`、`pnpm test`。PostgreSQL 真实测试读取环境变量 `NEXTERM_TEST_PG_DSN`，未设置时自动跳过。桌面端与服务端安装包的完整打包见 `node scripts/build.mjs --help`。

## 常用配置

服务端命令行选项优先于同名环境变量。完整参数可运行 `nexterm-server --help` 查看。

| 命令行选项 | 环境变量 | 用途 |
| --- | --- | --- |
| `--listen` | `NEXTERM_LISTEN` | 监听地址。直接运行二进制时默认为 `0.0.0.0:8080`；安装包的 systemd 配置使用 `127.0.0.1:8080`。 |
| `--data-dir` | `NEXTERM_DATA_DIR` | 数据库、日志等数据的保存目录。 |
| `--web-root` | `NEXTERM_WEB_ROOT` | 浏览器界面的静态文件目录，仅同步模式不需要。 |
| `--auth` | `NEXTERM_AUTH` | 访问控制：`on`（默认，任何监听地址都要求登录账号）、`loopback`（仅回环监听免登录，需显式指定）、`off`（关闭，仅适合本地共享工作区：账号、管理与设备路由全部关闭，库中已有任何用户账号时拒绝启动）。仅影响完整模式的 `/rpc`、`/ws`、`/files/blob`；`--sync-only` 下除 `/auth/status`、`/auth/init`、`/auth/login`、`/auth/register`、`/auth/recovery/reset`、`/auth/devices/enroll` 公共端点外，账号、超管与同步路由都要求登录会话。 |
| `--db` | `NEXTERM_DB` | 服务端数据库后端：`sqlite`（默认）或 `postgres`。`postgres` 运行内嵌的 `migrations/postgres` schema，当前版本只支持单写入实例。 |
| `--db-dsn` | `NEXTERM_DB_DSN` | PostgreSQL 连接串，如 `postgres://user@host:5432/nexterm?sslmode=verify-full&sslrootcert=/path/ca.crt`。要求 `--db=postgres`。 |
| `--db-password-file` | `NEXTERM_DB_PASSWORD_FILE` | 从 `0600` 文件读取 PostgreSQL 密码；DSN 已带密码时拒绝。 |
| `--db-max-open-conns` | `NEXTERM_DB_MAX_OPEN_CONNS` | PostgreSQL 连接池大小，默认 16。 |
| `--master-key` | `NEXTERM_MASTER_KEY` | 凭据库根密钥，至少 8 个字符。已弃用，请改用 `--master-key-file`。 |
| `--master-key-file` | `NEXTERM_MASTER_KEY_FILE` | 从文件读取凭据库根密钥（推荐；与 `--master-key` 互斥）。 |
| `--require-vault` | — | 启动时凭据库未能解锁则以非零状态退出。 |
| `--sync-only` | — | 只启动账号、超管与同步路由（`/auth/*`、`/admin/*`、`/sync/v2/*`、`/sync/rpc`），无浏览器界面。`/healthz` 的 `commands` 字段在此模式下上报对端同步命令数（3）。 |
| `--public-base-url` | `NEXTERM_PUBLIC_BASE_URL` | 仅完整模式。生成图片公开链接时使用的外部基础 URL，如 `https://term.example.com/nexterm`（反代带路径前缀时）。仅接受 http/https，拒绝 userinfo/query/fragment；未设置时生成同源相对链接。运行时可在「设置 → 文件链接」中覆盖（数据库存储优先于此默认值）。与同步地址、AI 模型地址互不影响。 |
| — | `NEXTERM_GATEWAY_AUTH` | 可选。设置后，携带匹配 `X-NexTerm-Gateway-Auth` 请求头的请求视为已通过前置网关鉴权，免登录会话（懒猫微服由网关注入该头）。自建部署请勿设置，设置后请像密钥一样保管。 |
| — | `NEXTERM_BLOB_MAX_BYTES` | 仅完整模式。单个文件上传的大小上限（字节），默认 268435456（256 MiB）。 |
| — | `NEXTERM_BLOB_PERSIST_MAX_BYTES` | 仅完整模式。持久保存文件的总配额（字节），默认 1073741824（1 GiB）。 |
| — | `NEXTERM_BLOB_DISK_MAX_PERCENT` | 仅完整模式。数据目录所在磁盘的使用率阈值（取值 (0, 100]），达到后拒绝新的持久化上传，默认 90。 |
| — | `NEXTERM_IMAGE_MAX_BYTES` | 仅完整模式。单张图片的大小上限（字节），默认 20971520（20 MiB）。 |
| — | `NEXTERM_IMAGE_OWNER_MAX_BYTES` | 仅完整模式。每个归属者的图片总配额（字节），默认 209715200（200 MiB）。 |
| — | `NEXTERM_IMAGE_TTL` | 仅完整模式。图片公开链接的有效期（Go 时长，如 `24h`），默认 24h，上限 7d。 |

`NEXTERM_BLOB_*` 与 `NEXTERM_IMAGE_*` 在启动时读取，无效值会被忽略并记录警告，回退到默认值。

### 图片限时公开链接

完整模式提供 `/files/image` 路由族，用于把终端里的图片以限时公开链接分享：上传（`POST /files/image`，要求登录会话与 CSRF 头）后返回不可猜测的链接 ID，默认 24 小时后自动过期清理（上限 7 天）。下载（`GET /files/image/{id}`）无需任何凭据即可在 `<img>` 或浏览器中直接打开，但只接受 png/jpeg/gif/webp（按内容嗅探，响应带 `X-Content-Type-Options: nosniff`，仅 inline 展示），且不会读取持久化文件区（`dataDir/files`）中的任何内容。删除（`DELETE /files/image/{id}`）仅限上传者本人或超管。上传、下载、删除与过期清理都会写审计（不含任何令牌）。

`--auth loopback` 下 Host 被限制为回环地址，公开链接经域名访问会收到 421，因此该模式只适合本机使用；对外分享请使用默认 `--auth on` 并配置 `--public-base-url`（或「设置 → 文件链接」中的同名项）指向你的 HTTPS 反代入口。`--auth off` 时图片归属共享本地工作区身份，不因此获得任何账号管理能力。

安装包中的 `nexterm-server.service` 与 `nexterm-onlyserver.service` 二选一，不要同时启用。完整版密钥放在 `/etc/nexterm/nexterm.env`，仅同步运行的密钥放在 `/etc/nexterm/onlyserver.env`，权限均设为 `0600`；不要把密钥直接写进可公开读取的 unit 文件。

`NEXTERM_MASTER_KEY` 环境变量已弃用（进程环境对同机用户可见），请改用 `--master-key-file /etc/nexterm/master.key`（或 `NEXTERM_MASTER_KEY_FILE`），文件权限设为 `0600`。对凭据同步有强依赖的部署可加 `--require-vault`：启动时凭据库未能解锁（未配置密钥或密钥错误）会直接以非零状态退出。

`/healthz` 无需登录，响应中的 `vault` 字段报告凭据库状态（`initialized`、`mode`、`unlocked`），可用于监控凭据库是否可用。

请备份密钥文件和数据目录（默认 SQLite 后端）；`--db postgres` 部署的工作区与同步数据存放在 PostgreSQL 中，必须另行备份数据库。更换根密钥后，已有的密码类凭据将无法解密。

## 常见问题

### macOS 提示无法验证开发者

打开「系统设置 → 隐私与安全性」，在安全性区域找到 NexTerm，点击「仍要打开」。如果提示应用「已损坏」，先升级到最新版本；仍出现时可在终端执行：

```bash
xattr -dr com.apple.quarantine /Applications/NexTerm.app
```

### 浏览器无法访问服务端

先在服务器本机检查健康接口和服务状态：

```bash
curl -fsS http://127.0.0.1:8080/healthz
systemctl status nexterm-server
```

`--sync-only` 模式没有浏览器界面，健康检查通过即可。如果本机检查正常但外部无法访问，请检查监听地址、防火墙以及反向代理的鉴权和 TLS 配置，不要直接放开公网端口来代替排查。

### 保存密码或同步凭据失败

确认服务已通过 `--master-key-file`（或已弃用的 `NEXTERM_MASTER_KEY`）配置根密钥，并且升级或迁移后仍使用原来的密钥和数据目录。同步失败时，还要检查服务端地址、HTTPS 证书和账号用户名、密码是否正确。

### 关闭标签后终端去哪了

关闭标签不再弹三选一。SSH 守护终端直接转入「后台会话」继续运行，之后可在「后台会话」里接管；本机、WinRM 非交互与容器 exec 等普通终端不支持转入后台，关闭时会先确认一次，确认后进程结束、无法恢复。要主动结束守护终端的进程，用标签菜单里的「结束进程」。

### 终端历史会记录什么

每条终端会话都会自动录制，按时间顺序记入本地数据库：终端输出、你在终端里键入的键盘输入、窗口尺寸变化。在「终端历史」里可以按文本查看输出、搜索关键词，或切到回放模式按录制时间轴重放；文本视图与回放只呈现输出（回放同时呈现窗口尺寸变化），键盘输入不会显示在界面上。但要注意：键盘输入会原样进入记录本身 —— 在终端里输入的密码、令牌等敏感内容同样会被记录，敏感操作前请留意。记录默认只保存在本机、不参与账号同步；需要时可在「终端历史」里对单条记录开启同步（端到端加密），也可以随时逐条删除。

## License

[MIT](LICENSE)
