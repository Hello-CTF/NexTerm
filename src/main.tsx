import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App from "./app/App";
import "./styles.css";
import { listenEvent, EVENTS, EventVersionGate, type SessionStatusEvent, type TerminalExitEvent } from "./ipc/events";
import { onEventsResync } from "./ipc/webTransport";
import { mountApi, systemApi } from "./ipc/commands";
import { useUi } from "./app/store";
import { onRemoteChange } from "./app/layout";
import { setMacPlatform } from "./app/platform";
import { setMountUnavailableReason } from "./app/capabilities";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

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

onEventsResync(() => {
  void useUi.getState().resyncSessions();
  onRemoteChange(null);
});

window.addEventListener("contextmenu", (e) => {
  const el = e.target as HTMLElement | null;
  const inEditable = el?.closest("input, textarea, [contenteditable='true']");
  const inTerminal = el?.closest(".xterm");
  if (inEditable && !inTerminal) return;
  e.preventDefault();
});

async function bootstrap() {
  try {
    const [os, mountReason] = await Promise.all([
      systemApi.platform(),
      mountApi.capability(),
    ]);
    if (typeof os === "string" && os.length > 0) {
      setMacPlatform(os === "macos");
    }
    setMountUnavailableReason(mountReason);
  } catch {
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
