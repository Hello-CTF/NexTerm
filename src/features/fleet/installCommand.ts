// 全新机器一键安装命令构造 (FLEET168): curl 拉取选中接入地址的
// /install-device.sh (M157), 用一次性接入码与真实服务端版本执行。
// 合同对齐 public/install-device.sh 的用法行: --server/--code/--version 必填,
// --insecure 跟随接入地址配置。版本来自 /healthz, 由调用方保证非空 — 版本
// 不可用时整段 UI 必须隐藏, 绝不伪造版本, 也绝不声称安装成功。

// shellQuote 把动态参数 (接入地址/接入码/版本) 包成单引号安全形式, 防止
// URL path 中的 ; & $() 等被 shell 当命令语法 (服务端 normalizeBaseURL 允许
// 这些字符)。
export function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

// insecure 仅在该接入地址显式配置时放宽证书: 下载脚本的 curl 同样加
// --insecure (自签名 HTTPS 下否则管道左侧先证书失败, 脚本根本收不到内容),
// 并保留传给脚本/enroll 的 --insecure; 未配置 insecure 的地址两侧都严格校验。
export function oneLineInstallCommand(baseUrl: string, insecure: boolean, code: string, version: string): string {
  const scriptUrl = `${baseUrl}/install-device.sh`;
  return `curl -fsSL${insecure ? " --insecure" : ""} ${shellQuote(scriptUrl)} | sh -s -- --server ${shellQuote(baseUrl)} --code ${shellQuote(code)} --version ${shellQuote(version)}${insecure ? " --insecure" : ""}`;
}

// psQuote 是 PowerShell 字面字符串引用: 单引号包裹, 内嵌单引号双写转义
// (shellQuote 的 '\'' 形态在 PowerShell 里不合法)。
export function psQuote(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

// Windows 设备走 PowerShell 版 install-device.ps1: iex 包 scriptblock 的形态
// 才能向远端脚本透传 --server/--code/--version (与 sh 版同名参数); 直管道
// iwr ... | iex 无法带参数。ps1 由独立交付物提供, 此处只约定 URL 与参数形态。
export function oneLineInstallCommandWindows(baseUrl: string, insecure: boolean, code: string, version: string): string {
  const scriptUrl = `${baseUrl}/install-device.ps1`;
  return `iex "& { $(iwr -UseBasicParsing ${psQuote(scriptUrl)}) } --server ${psQuote(baseUrl)} --code ${psQuote(code)} --version ${psQuote(version)}${insecure ? " --insecure" : ""}"`;
}
