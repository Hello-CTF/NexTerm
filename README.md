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

- **终端与文件**：SSH、WinRM 和本机终端，支持多标签、分屏、SFTP 文件管理与在线编辑。
- **日常运维**：管理 Docker 容器与镜像，使用 MySQL、Redis 和 SSH 端口转发。
- **AI 助手**：接入 OpenAI 兼容接口；命令输出和文件变更可见，敏感操作需要确认。
- **桌面与浏览器**：桌面端适合本机使用；服务端的终端进程和工作区保存在服务器上，关闭网页后可继续。
- **资产同步**：在桌面端与服务端之间推送或拉取资产、分组和凭据，不必重复配置。

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

首次使用时，添加 SSH 或 WinRM 资产，也可以直接使用内置的「当前设备」。按界面提示初始化凭据库后再保存密码或私钥。需要使用 AI 时，在设置中填写 OpenAI 兼容接口地址、API Key 和模型名称。

## 服务端与懒猫微服

### LinuxServer

LinuxServer 适合部署在常开的 Linux 机器上。解压后，按照包内 `README.md` 安装二进制、网页文件和所需的 systemd 服务。以 amd64 包为例：

```bash
tar xzf NexTerm-server_x.y.z_linux_amd64.tar.gz
cd NexTerm-server_x.y.z_linux_amd64
less README.md
```

安装包的 systemd 配置默认监听 `127.0.0.1:8080`，并显式声明 `--auth on`（任何访问都要同步令牌）。启动后在本机打开 `http://127.0.0.1:8080`，或通过带鉴权的 HTTPS 反向代理访问。

> **不要把完整版服务端直接暴露到公网。** 访问控制默认开启（`--auth on`，rc3 起）：无论监听地址，`/rpc`、`/ws` 与 `/files/blob` 都要求同步令牌（`/healthz` 与页面静态资源保持公开），浏览器首次打开会提示输入令牌，可用 `nexterm-server token` 查看。回环豁免仅在显式指定 `--auth loopback` 时生效，且该模式校验 Host 只允许 `localhost`、`127.0.0.1`、`[::1]`（防 DNS 重绑定），不匹配返回 421。对外访问时，仍建议只监听回环地址，并在前面配置带 TLS 的反向代理。

### 仅同步运行

在同一个 `nexterm-server` 上启用 `--sync-only`，即可只提供资产同步：

```bash
nexterm-server --sync-only --listen 127.0.0.1:8080 --data-dir /var/lib/nexterm
```

该模式只开放 `/sync/rpc` 和 `/healthz`，RPC 仅有 `sync_digest`、`sync_export`、`sync_import` 三条，不提供浏览器界面或 `/rpc`。同步令牌泄漏的影响因此限于这份资产库，不会获得终端、文件或容器控制接口。

使用 `nexterm-server token --data-dir /var/lib/nexterm` 获取令牌，然后在桌面端的「设置 → 资产同步」中填写服务端地址和令牌。浏览器版首次打开时提示输入的也是这枚令牌。公网同步必须使用 HTTPS，并妥善保管令牌。需要换令牌时使用 `rotate-token`；旧令牌会立即失效，桌面端也要同步更新。

### 从 rc2 升级（rc3 默认变化）

rc3 把完整版访问控制的默认值从 "仅非回环监听强制" 翻转为 `--auth on`（任何监听地址都强制），仓库自带的 systemd 单元也显式使用 `--auth on`。升级前请对照部署形态处理：

- 监听 `0.0.0.0` 等非回环地址的现有部署：原本就强制令牌，行为不变，无需处理。
- 监听回环地址的现有部署：升级后浏览器首次打开会提示输入同步令牌（`nexterm-server token` 查看），这是推荐的默认行为。如确需恢复回环免令牌，请自行显式设置 `--auth loopback` 或 `NEXTERM_AUTH=loopback`。
- `--auth loopback` 仅适合直接本机访问：它把 Host 限制为 `localhost`、`127.0.0.1`、`[::1]`，经反向代理以域名访问会收到 421。反向代理部署请保持默认 `--auth on`。
- 确需关闭访问控制可显式 `--auth off`（强烈不建议；启动日志会给出警告）。

### 懒猫微服

从应用中心安装后直接打开 NexTerm。微服会注入凭据库所需的根密钥，终端进程和数据保存在你的微服上。

懒猫微服不提供 NexTerm 内置端口转发；需要对外提供远端端口时，请使用微服平台自带的转发功能。

## 常用配置

服务端命令行选项优先于同名环境变量。完整参数可运行 `nexterm-server --help` 查看。

| 命令行选项 | 环境变量 | 用途 |
| --- | --- | --- |
| `--listen` | `NEXTERM_LISTEN` | 监听地址。直接运行二进制时默认为 `0.0.0.0:8080`；安装包的 systemd 配置使用 `127.0.0.1:8080`。 |
| `--data-dir` | `NEXTERM_DATA_DIR` | 数据库、日志和同步令牌等数据的保存目录。 |
| `--web-root` | `NEXTERM_WEB_ROOT` | 浏览器界面的静态文件目录，仅同步模式不需要。 |
| `--auth` | `NEXTERM_AUTH` | 访问控制：`on`（默认，任何监听地址都强制同步令牌）、`loopback`（仅回环监听豁免，需显式指定）、`off`（关闭，不建议）。仅影响完整模式的 `/rpc`、`/ws`、`/files/blob`；`/sync/rpc` 始终要求同步令牌。 |
| `--master-key` | `NEXTERM_MASTER_KEY` | 凭据库根密钥，至少 8 个字符。已弃用，请改用 `--master-key-file`。 |
| `--master-key-file` | `NEXTERM_MASTER_KEY_FILE` | 从文件读取凭据库根密钥（推荐；与 `--master-key` 互斥）。 |
| `--require-vault` | — | 启动时凭据库未能解锁则以非零状态退出。 |
| `--sync-only` | — | 只启动资产同步接口。 |
| — | `NEXTERM_GATEWAY_AUTH` | 可选。设置后，携带匹配 `X-NexTerm-Gateway-Auth` 请求头的请求视为已通过前置网关鉴权，免同步令牌（懒猫微服由网关注入该头）。自建部署请勿设置，设置后请像密钥一样保管。 |

安装包中的 `nexterm-server.service` 与 `nexterm-onlyserver.service` 二选一，不要同时启用。完整版密钥放在 `/etc/nexterm/nexterm.env`，仅同步运行的密钥放在 `/etc/nexterm/onlyserver.env`，权限均设为 `0600`；不要把密钥直接写进可公开读取的 unit 文件。

同步令牌以明文保存在数据目录的数据库中：备份数据目录等于备份同步令牌，请像保管密钥一样保管备份。存放 `NEXTERM_MASTER_KEY` 的 EnvironmentFile 必须保持 `0600` 且属主为运行用户，避免同机其他用户读取。

`NEXTERM_MASTER_KEY` 环境变量已弃用（进程环境对同机用户可见），请改用 `--master-key-file /etc/nexterm/master.key`（或 `NEXTERM_MASTER_KEY_FILE`），文件权限设为 `0600`。对凭据同步有强依赖的部署可加 `--require-vault`：启动时凭据库未能解锁（未配置密钥或密钥错误）会直接以非零状态退出。

请备份密钥文件和数据目录。更换根密钥后，已有的密码类凭据将无法解密。

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

确认服务已通过 `--master-key-file`（或已弃用的 `NEXTERM_MASTER_KEY`）配置根密钥，并且升级或迁移后仍使用原来的密钥和数据目录。同步失败时，还要检查服务端地址、HTTPS 证书和同步令牌；令牌轮换后必须更新桌面端保存的令牌。

## License

[MIT](LICENSE)
