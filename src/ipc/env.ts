// 运行环境判定（三态）。
//
// # 为什么需要它
//
// 前端原本只有两态：**Tauri 容器** 与 **纯浏览器（= 演示模式）**，
// 判据是 `window.__TAURI_INTERNALS__` 在不在。加了 nexterm-server 之后，
// 「纯浏览器」就一分为二了：
//
//   · `pnpm dev` 打开的普通浏览器  → 演示模式（假数据，无后端）
//   · 被 nexterm-server 服务的页面 → **真后端**，数据是真的
//
// 两者 `__TAURI_INTERNALS__` 都是 `undefined`，所以必须另给一个判据。
// 用「探测 /rpc 通不通」是异步的，而模块求值（`commands.ts` / `events.ts` 顶层）
// 时就必须有答案。所以服务端在发 `index.html` 时注入一行标记
// （见 Rust 侧 `server/static_files.rs`），这里同步读取。

export type Transport = "desktop" | "web" | "demo";

function detect(): Transport {
  if (typeof window === "undefined") return "desktop";
  const w = window as unknown as Record<string, unknown>;
  const isTauri = w.__TAURI_INTERNALS__ !== undefined;
  const isWeb = w.__NEXTERM_TRANSPORT__ === "web";

  let flag: string | null = null;
  try {
    flag = new URLSearchParams(window.location.search).get("demo");
  } catch {
    // 解析失败按自动判定处理
  }

  // `?demo=1` 强制演示模式：在真后端上也能跑假数据，排 UI 问题时很有用。
  if (flag === "1") return "demo";
  if (isTauri) return "desktop";
  if (isWeb) return "web";
  // 普通浏览器、没有服务端标记 ⇒ 演示模式（`pnpm dev` 的既有工作流）。
  return "demo";
}

/** 当前运行环境。 */
export const TRANSPORT: Transport = detect();

/** 演示模式：数据全在内存里，不连任何真实服务器。 */
export const DEMO = TRANSPORT === "demo";

/** 服务端模式：浏览器 + nexterm-server（真后端）。 */
export const WEB = TRANSPORT === "web";

/** 桌面模式：Tauri 容器内。 */
export const DESKTOP = TRANSPORT === "desktop";

/**
 * 客户端身份：一台设备上的一个浏览器实例。
 *
 * # 为什么需要它
 *
 * 终端是「多点可看、单点可写」—— 同时看着的人得有个**稳定的名字**，否则
 * 刷新一次页面就"换了个人"，键盘控制权会在没有任何操作的情况下易主（用户看到
 * 的现象是"莫名其妙敲不进去字了"）。
 *
 * 存 `localStorage` 而不是内存变量：刷新要保住身份。新开的标签页也应当算
 * **同一个客户端**（同一台设备上多开几个页面，不该被当成两台设备互相抢键盘）。
 *
 * # 与「通道 id」的区别
 *
 * 通道 id 每个标签页一条（页面关了就没了），身份要跨页面稳定 —— 两者不能合成
 * 一个值。服务端的 `TerminalTab` 里也是分开存的（见 `terminal/mod.rs` 的 `Sink`）。
 */
const CLIENT_KEY = "nexterm.client";

let memoryClientId = "";

export function clientId(): string {
  try {
    let v = window.localStorage.getItem(CLIENT_KEY);
    if (!v) {
      v = newClientId();
      window.localStorage.setItem(CLIENT_KEY, v);
    }
    return v;
  } catch {
    // 无痕模式 / 存储被禁用：退回进程内一次性 id。
    // 代价要认下来 —— 此时刷新页面会被当成新设备，控制权需要重新接管。
    if (!memoryClientId) memoryClientId = newClientId();
    return memoryClientId;
  }
}

/** 只含字母数字：通道 id 会被塞进 URL 路径，任何需要转义的字符都会出问题。 */
function newClientId(): string {
  return `d${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
}

/**
 * 后端基址（仅服务端模式用）。
 *
 * 默认同源 —— 生产环境前端就是 nexterm-server 自己发的，同源是对的。
 * 之所以还留一个覆盖入口：`pnpm dev`（vite 在 1420）对着远端盒子上的
 * 服务端调界面时，同源不成立。用 `?api=http://host:port` 或
 * `localStorage.nexterm.api` 指过去即可，免得为此改构建。
 */
export function apiBase(): string {
  if (typeof window === "undefined") return "";
  try {
    const fromQuery = new URLSearchParams(window.location.search).get("api");
    if (fromQuery) return fromQuery.replace(/\/$/, "");
    const fromStore = window.localStorage.getItem("nexterm.api");
    if (fromStore) return fromStore.replace(/\/$/, "");
  } catch {
    // 隐私模式下 localStorage 会抛，忽略即可
  }
  return "";
}

/** 同源时走 `location.host`；否则用 `apiBase()` 推导。 */
export function httpUrl(path: string): string {
  const base = apiBase();
  return base ? `${base}${path}` : path;
}

export function wsUrl(path: string): string {
  const base = apiBase();
  if (base) {
    return `${base.replace(/^http/, "ws")}${path}`;
  }
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}${path}`;
}
