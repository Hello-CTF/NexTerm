// 演示模式入口。
//
// 判定规则：
//   · URL 带 `?demo=1`  → 强制开启；`?demo=0` → 强制关闭；
//   · 否则：不在 Tauri 容器里（即纯浏览器跑 `pnpm dev`）就自动开启。
//
// 之所以放在浏览器自动开启：Tauri 的 `Channel` / `listen` 都依赖
// `window.__TAURI_INTERNALS__`，纯浏览器里直接调用会抛异常；
// 用演示模式接管后，前端在浏览器里可以完整跑起来，方便逐面板验收。
//
// 注意：这里**不静态 import** `./mock`。假数据 + 虚拟 shell + 命令分发加起来约 60KB，
// 打进 Tauri 生产包纯属浪费；改由 `ipc/commands.ts` 在演示模式下动态加载，
// 于是它们自成一个懒加载 chunk，真机永远不会请求到。

function detect(): boolean {
  if (typeof window === "undefined") return false;
  try {
    const flag = new URLSearchParams(window.location.search).get("demo");
    if (flag === "1") return true;
    if (flag === "0") return false;
  } catch {
    // 解析失败按自动判定处理
  }
  const w = window as unknown as Record<string, unknown>;
  return w.__TAURI_INTERNALS__ === undefined;
}

/** 是否处于演示模式。 */
export const DEMO = detect();

if (DEMO) {
  console.warn(
    "[NexTerm] 演示模式：所有数据都是内存里的假数据，不会连接任何真实服务器。\n" +
      "URL 加 ?demo=0 可关闭（需要真实 Tauri 环境）。",
  );
}

export { emit, subscribe, pushText, pushEvent, later } from "./bus";
