/**
 * 平台判定（仅界面细节：标题栏按钮、示例文案）。后端能力差异由 Rust cfg 决定，
 * 前端不做能力判断。
 *
 * 可靠性排序：后端 `app_platform` 命令（编译期常量）> UA 猜测（浏览器 demo 兜底）。
 * WKWebView 经自定义协议加载时 UA 可能不含平台标识，曾把 macOS 误判成 Windows。
 * main.tsx 会在首次渲染**之前**用后端结果校正 —— 渲染后再翻会闪一下 Windows 布局。
 */
let macPlatform =
  typeof navigator !== "undefined" &&
  (/Mac/i.test(navigator.platform ?? "") || /Macintosh/.test(navigator.userAgent));

export function isMac(): boolean {
  return macPlatform;
}

/** 渲染前由 main.tsx 调用：用后端的编译期平台覆盖 UA 猜测。 */
export function setMacPlatform(fromBackend: boolean): void {
  macPlatform = fromBackend;
}
