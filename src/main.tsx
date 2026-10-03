// NexTerm 前端入口：全局事件接线 + 根组件。
import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App from "./app/App";
import "./styles.css";
import { listenEvent, EVENTS, EventVersionGate, type SessionStatusEvent, type TerminalExitEvent } from "./ipc/events";
import { mountApi, systemApi } from "./ipc/commands";
import { useUi } from "./app/store";
import { setMacPlatform } from "./app/platform";
import { setMountUnavailableReason } from "./app/capabilities";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

// 全局事件 → store（§6.3）
// 版本高水位：重连后内核可能把旧事件重放过来，按会话/标签记下已见的最大
// version，旧版本直接丢弃，避免状态回退（重复弹退出提示、状态徽标回跳）。
const eventVersions = new EventVersionGate();

void listenEvent<SessionStatusEvent>(EVENTS.sessionStatus, (p) => {
  if (!eventVersions.accept(p.sessionId, p.version)) return;
  const { sessions, setSessions, pushToast } = useUi.getState();
  setSessions(
    sessions.map((s) =>
      s.id === p.sessionId ? { ...s, status: p.status as SessionStatusEvent["status"] as never } : s,
    ),
  );
  if (p.status === "failed" && p.error) {
    pushToast("error", `会话失败: ${p.error}`);
  }
});

void listenEvent<TerminalExitEvent>(EVENTS.terminalExit, (p) => {
  if (!eventVersions.accept(p.tabId, p.version)) return;
  const { pushToast } = useUi.getState();
  if (p.exitCode !== null && p.exitCode !== 0) {
    pushToast("info", `终端进程退出（exit ${p.exitCode}）`);
  }
});

void listenEvent<{ code: string; message: string }>(EVENTS.appError, (p) => {
  useUi.getState().pushToast("error", p.message);
});

// ── 全局右键：干掉 WebView 自带的浏览器菜单 ──
// Tauri 下弹的是「返回 / 前进 / 重新加载 / 检查元素」，在运维终端里毫无意义，
// 还会盖住我们自己的菜单（src/ui/ContextMenu.tsx）。
//
// 输入类控件要放行：复制 / 粘贴 / 全选在那里是刚需，自绘一套反而丢功能。
// 但 xterm 的 `.xterm-helper-textarea`（那个透明输入代理）本身就是 textarea，
// 而终端恰恰是最需要自绘菜单的地方 —— 所以得先看它在不在 .xterm 里。
window.addEventListener("contextmenu", (e) => {
  const el = e.target as HTMLElement | null;
  const inEditable = el?.closest("input, textarea, [contenteditable='true']");
  const inTerminal = el?.closest(".xterm");
  if (inEditable && !inTerminal) return;
  e.preventDefault();
});

// ── 平台判定 + 能力开关：先问后端（编译期常量），再渲染 ──
// WKWebView 的 UA 不可靠（自定义协议下可能不含平台标识），渲染后才校正
// 会导致标题栏闪变，所以这里阻塞首帧等这一个 IPC。
// 能力开关同批拉取：磁盘挂载在 macOS 上是「暂不可用」，入口需要在首帧就置灰，
// 否则会先画出一个可点的按钮再跳成灰色。
//
// 走 `src/ipc/commands.ts` 而不是直接 `invoke`：这样三种运行环境
// （桌面 / 服务端 / 演示）共用同一条出海口，不至于在这里漏掉服务端模式。
async function bootstrap() {
  try {
    const [os, mountReason] = await Promise.all([
      systemApi.platform(),
      mountApi.capability(),
    ]);
    // 演示模式没有真后端：mock 对这两条命令返回 null，此时**不要**覆盖
    // UA 猜测 —— 否则 macOS 上跑演示模式会被判成 Windows。
    if (typeof os === "string" && os.length > 0) {
      setMacPlatform(os === "macos");
    }
    setMountUnavailableReason(mountReason);
  } catch {
    /* 后端还没就绪：维持 UA 探测 + 能力默认可用 */
  }
  ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
    <React.StrictMode>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </React.StrictMode>,
  );
}

void bootstrap();
