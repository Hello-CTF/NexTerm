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

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </React.StrictMode>,
);
