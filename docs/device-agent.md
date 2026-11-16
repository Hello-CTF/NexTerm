# 设备接入

**中文** | [English](device-agent.en.md) · [返回 README](../README.md)

设备代理让服务端管理一台设备：设备主动外连服务端，上报 CPU、内存、磁盘指标，并提供终端和文件访问。

## 支持的平台

Release 提供 Linux（amd64 / arm64）、macOS（amd64 / arm64）和 Windows（amd64 / arm64）的设备代理。

## 接入

1. 超级管理员在「设备管理」配置接入地址并签发一次性接入码。
2. 在设备上执行面板给出的一行命令：Linux/macOS 用普通用户（不需要 root），Windows 用 PowerShell。脚本按同一版本的 `SHA256SUMS` 校验下载后才安装。
3. 设备代理随用户登录自动启动（Linux 为 systemd 用户服务，macOS 为 launchd，Windows 为登录计划任务）。

## Shell 与环境变量

设备代理在运行时解析账户的登录 shell 及其环境（先以交互式登录 shell 读取，失败时退回登录 shell），不在服务定义里记录安装时的 `SHELL` 或 `PATH`。安装时可用 `--shell <绝对路径>` 指定终端使用的 shell，`--shell auto` 清除设置并改回账户登录 shell。`nexterm-agent status` 显示设备、服务和持久终端组件的状态，加 `--verbose` 时还显示当前使用的 shell 和解析结果；旧版安装曾在服务定义中记录 `SHELL`/`PATH`，重新执行安装命令即可清除。

## 自升级

设备代理自升级前验证 Minisign 签名，未内置签名公钥的开发构建会拒绝自升级。服务端报告协议版本不兼容时，设备代理自动升级到服务端的版本，也可在设备上执行 `nexterm-agent update`；下载只来自官方 Release。

替换前，设备代理先运行下载的新程序自检（`nexterm-agent version`），自报版本与目标版本一致才替换，旧程序保留为 `nexterm-agent.nexterm-old`。新版本须在 120 秒内首次连上服务端确认，否则自动恢复旧程序，并在设备管理中显示为「后台服务出错」；之后服务端再次要求该版本时，第一次失败后等 1 小时，每次失败加倍，最长 24 小时，到期自行重试，也可执行 `nexterm-agent update --version <版本>` 提前手动重试。没有安装服务、重启失败或机器在等待期间重启时不做回滚，保留新程序。

替换和重启的结果记录在数据目录的 `upgrade.log`。
