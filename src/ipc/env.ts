// 运行环境判定（三态）。
//
// # 为什么需要它
//
// Wails、被 nexterm-server 服务的页面和普通浏览器必须同步区分：
// 模块求值（`commands.ts` / `events.ts` 顶层）时就要有答案，不能异步探测 /rpc。
// 桌面优先读显式 `__NEXTERM_TRANSPORT__="desktop"`，也接受 Wails 注入的
// `window.wails`；不能读 `_wails`，因为 npm runtime 在普通浏览器也会创建它。

export type Transport = "desktop" | "web" | "demo";

function detect(): Transport {
  if (typeof window === "undefined") return "desktop";
  const w = window as unknown as Record<string, unknown>;
  const marker = w.__NEXTERM_TRANSPORT__;
  const isWails = marker === "desktop" || w.wails !== undefined;
  const isWeb = marker === "web";

  let flag: string | null = null;
  try {
    flag = new URLSearchParams(window.location.search).get("demo");
  } catch {
    // 解析失败按自动判定处理
  }

  // `?demo=1` 强制演示模式：在真后端上也能跑假数据，排 UI 问题时很有用。
  if (flag === "1") return "demo";
  if (isWails) return "desktop";
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

/** 桌面模式：Wails 容器内。 */
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
