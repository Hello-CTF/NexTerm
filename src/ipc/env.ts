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
