// NexTerm 前端入口：全局事件接线 + 根组件。
import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App from "./app/App";
import "./styles.css";
import { listenEvent, EVENTS, type SessionStatusEvent } from "./ipc/events";
import { useUi } from "./app/store";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

// 全局事件 → store（§6.3）
void listenEvent<SessionStatusEvent>(EVENTS.sessionStatus, (p) => {
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

void listenEvent<{ tabId: string; exitCode: number | null }>(EVENTS.terminalExit, (p) => {
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

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </React.StrictMode>,
);
