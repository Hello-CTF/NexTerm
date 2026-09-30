// 演示模式入口。
//
// 判定规则集中在 `src/ipc/env.ts`（三态：desktop / web / demo）——
// 因为加了 nexterm-server 之后「纯浏览器」不再等于「演示模式」了，
// 而这两者原先共用 `__TAURI_INTERNALS__` 这一个判据。这里只做转出 + 提示。
//
// 判定规则（详见 env.ts）：
//   · URL 带 `?demo=1`  → 强制演示；`?demo=0` → 不强制
//   · 在 Tauri 容器里     → desktop
//   · 页面带服务端注入的标记 → web（真后端）
//   · 以上都不是          → demo
//
// 注意：这里**不静态 import** `./mock`。假数据 + 虚拟 shell + 命令分发加起来约 60KB，
// 打进 Tauri 生产包纯属浪费；改由 `ipc/commands.ts` 在演示模式下动态加载，
// 于是它们自成一个懒加载 chunk，真机永远不会请求到。

import { DEMO, TRANSPORT, WEB } from "../ipc/env";

// 演示模式**必须**喊一声：用户看到的是假数据，这直接影响他会不会当真去操作。
if (DEMO) {
  console.warn(
    "[NexTerm] 演示模式：所有数据都是内存里的假数据，不会连接任何真实服务器。\n" +
      "URL 加 ?demo=0 可关闭（需要真实 Tauri 环境或 nexterm-server）。",
  );
}

// 服务端模式**刻意不打日志**：
//   · 对终端用户没有可操作性，却会在每个人的 devtools 里留一条（eslint 的
//     no-console 白名单只有 warn/error，正是为了挡这类"顺手打一条"）；
//   · 排障要看的是 `window.__NEXTERM_TRANSPORT__` 与 Network 面板的 /rpc 请求，
//     那才是判据 —— 一条启动日志反而会让人以为"打了日志就是走对了"。
// 所以这里只转出判定结果，不产生输出。

export { DEMO, WEB, TRANSPORT };
export { emit, subscribe, pushText, pushEvent, later } from "./bus";
