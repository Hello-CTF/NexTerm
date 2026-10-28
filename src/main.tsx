import { startFrontendProbeIfCapturing } from "./features/diagnostics/frontendProbe";
import React, { lazy } from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App from "./app/App";
import "./styles.css";
import { listenEvent, EVENTS, EventVersionGate, type AINotificationEvent, type AppErrorEvent, type ConnectProgressEvent, type HelperProgressEvent, type SessionStatusEvent, type TerminalExitEvent } from "./ipc/events";
import { onEventsResync } from "./ipc/webTransport";
import { mountApi, systemApi } from "./ipc/commands";
import { WEB } from "./ipc/env";
import { consumeDeepLinks } from "./ipc/wails";
import { openDeepLink } from "./app/deepLink";
import { applySessionStatusEvent, noteHelperProgress, probeForegroundSessions, useUi } from "./app/store";
import { installForegroundResume } from "./app/foregroundResume";
import { onRemoteChange } from "./app/layout";
import { setMacPlatform, setWindowsPlatform } from "./app/platform";
import { installLongPressContextMenu } from "./app/longPress";
import { setMountUnavailableReason } from "./app/capabilities";
import { forceResolvedTheme } from "./app/theme";
import { Lazy } from "./ui/LazyBoundary";
import { notifyAi } from "./features/inbox/inboxNotifications";

// Public share pages carry their own terminal/preview stack; keep it out of the main app's entry chunk.
const PublicPreviewApp = lazy(() => import("./features/sharing/PublicPreviewApp").then((m) => ({ default: m.PublicPreviewApp })));

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

const eventVersions = new EventVersionGate();

function publicPreviewTokenFromPath(pathname: string): string | null {
  const match = /^\/share\/preview\/([^/]+)\/?$/.exec(pathname);
  if (!match) return null;
  try {
    const token = decodeURIComponent(match[1]);
    return token.length > 0 ? token : null;
  } catch {
    return null;
  }
}

function mount(node: React.ReactNode): void {
  ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
    <React.StrictMode>{node}</React.StrictMode>,
  );
  void startFrontendProbeIfCapturing();
}

function wireDesktopShell(): void {
  void listenEvent<SessionStatusEvent>(EVENTS.sessionStatus, (p) => {
    if (!eventVersions.accept(p.sessionId, p.version)) return;
    applySessionStatusEvent(p);
  });

  void listenEvent<ConnectProgressEvent>(EVENTS.connectProgress, (p) => {
    useUi.getState().setConnectProgress(p.assetId, p);
  });

  void listenEvent<HelperProgressEvent>(EVENTS.helperProgress, (p) => {
    noteHelperProgress(p);
    useUi.getState().setHelperProgress(p);
  });

  void listenEvent<TerminalExitEvent>(EVENTS.terminalExit, (p) => {
    if (!eventVersions.accept(p.tabId, p.version)) return;
    const { pushToast } = useUi.getState();
    if (p.exitCode !== null && p.exitCode !== 0) {
      pushToast("info", `终端进程退出（exit ${p.exitCode}）`);
    }
  });

  void listenEvent<AppErrorEvent>(EVENTS.appError, (p) => {
    useUi.getState().pushToast("error", p.message);
  });

  void listenEvent<AINotificationEvent>(EVENTS.aiNotification, (p) => {
    useUi.getState().pushToast("info", `AI 通知：${p.text}`);
    notifyAi(p.text);
  });

  if (!WEB) {
    void listenEvent<string>(EVENTS.deepLink, (url) => void openDeepLink(url));
    void consumeDeepLinks().then((urls) => {
      for (const url of urls) void openDeepLink(url);
    });
  }

  // Returning to the foreground (tab shown, app resumed, machine woke, network back) checks the visible
  // sessions at once instead of waiting for the next heartbeat or reconnect backoff.
  installForegroundResume(() => void probeForegroundSessions());

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
  installLongPressContextMenu();
}

async function bootstrapApp(): Promise<void> {
  wireDesktopShell();
  void wireMobileInsets();
  try {
    const [os, mountReason] = await Promise.all([
      systemApi.platform(),
      mountApi.capability(),
    ]);
    if (typeof os === "string" && os.length > 0) {
      setMacPlatform(os === "macos" || os === "darwin");
      setWindowsPlatform(os === "windows");
    }
    setMountUnavailableReason(mountReason);
  } catch {
  }
  mount(
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>,
  );
}

async function wireMobileInsets(): Promise<void> {
  if (!/Android|iPhone|iPad/i.test(navigator.userAgent)) return;
  const apply = async () => {
    try {
      const insets = await systemApi.insets();
      const style = document.documentElement.style;
      style.setProperty("--nx-inset-top", `${insets.top}px`);
      style.setProperty("--nx-inset-right", `${insets.right}px`);
      style.setProperty("--nx-inset-bottom", `${insets.bottom}px`);
      style.setProperty("--nx-inset-left", `${insets.left}px`);
    } catch {
      // Keep the env()/0 values when system_insets is unavailable (server/web)
    }
  };
  await apply();
  window.addEventListener("resize", () => void apply());
  window.addEventListener("orientationchange", () => void apply());
}

const publicPreviewToken =
  typeof window === "undefined" ? null : publicPreviewTokenFromPath(window.location.pathname);

if (publicPreviewToken) {
  forceResolvedTheme("dark");
  mount(<Lazy><PublicPreviewApp token={publicPreviewToken} /></Lazy>);
} else {
  void bootstrapApp();
}
