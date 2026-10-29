import { memo, useEffect, useState } from "react";
import {
  useUi,
  useSessionList,
  reconnectRestoredTab,
  reconnectRestoredWorkspace,
  type AppTab,
  type DbKind,
} from "../store";
import { isMultiHostLogTabId } from "../../features/logs/logTailTab";
import { isFleetHealthTabId } from "../../features/fleet/fleetHealthTab";
import { Lazy } from "../../ui/LazyBoundary";
import {
  AuditView,
  BackgroundSessions,
  CredentialsPanel,
  DbPanel,
  DockerPanel,
  ExecBatchPanel,
  FileEditor,
  FleetHealthPanel,
  ForwardPanel,
  HostDetailView,
  LogViewer,
  MountPanel,
  MultiHostLogTail,
  SettingsView,
  TerminalPane,
  TranscriptHistoryPanel,
} from "../lazyPanels";
import { EmptyState } from "./EmptyStates";

// LazyDevicesView loads device management lazily: it reaches env through the auth store, and lazy loading keeps it out of the desktop test module graph.
function LazyDevicesView() {
  const [view, setView] = useState<React.ComponentType | null>(null);
  useEffect(() => {
    let alive = true;
    void import("../../features/fleet/DevicesView").then((m) => {
      if (alive) setView(() => m.DevicesView);
    });
    return () => {
      alive = false;
    };
  }, []);
  if (!view) return null;
  const View = view;
  return <View />;
}

// Shared manual-restore placeholder for restorePending tabs: nothing mounts and no
// connection is dialed until the user explicitly reconnects via reconnectRestoredTab.
function RestoredTabPlaceholder({ tab, workspaceId }: { tab: AppTab; workspaceId: string }) {
  const assetKind = useUi((state) => state.workspaces.find((workspace) => workspace.id === workspaceId)?.assetKind);
  const durableHost = tab.kind === "terminal" && (assetKind === "ssh" || assetKind === "device");
  const [busy, setBusy] = useState(false);
  const reopen = () => {
    if (busy) return;
    setBusy(true);
    void reconnectRestoredTab(tab.id)
      .then((result) => {
        if (!result.ok && result.error) useUi.getState().pushToast("error", result.error);
      })
      .finally(() => setBusy(false));
  };
  const openBackground = () => {
    if (busy) return;
    setBusy(true);
    void reconnectRestoredWorkspace(workspaceId).then((result) => {
      const state = useUi.getState();
      if (!result.ok) {
        if (result.error) state.pushToast("error", result.error);
        setBusy(false);
        return;
      }
      const workspace = state.workspaces.find((item) => item.id === workspaceId);
      if (!workspace?.sessionId) {
        setBusy(false);
        return;
      }
      state.addTab({ id: `background-${workspaceId}`, kind: "background", title: "后台会话", sessionId: workspace.sessionId, closable: true });
      setBusy(false);
    });
  };
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-neutral-400">
      <span>
        {tab.kind === "terminal"
          ? durableHost
            ? "该终端尚未连接。选择「新开终端」将打开新的终端；如原为持久终端，可在「后台会话」中接管。"
            : "原终端已结束。选择「新开终端」将打开新的终端。"
          : "连接主机后才能打开此标签页。"}
      </span>
      <div className="flex flex-wrap items-center justify-center gap-2">
        <button type="button" className="nx-btn nx-btn-outline" disabled={busy} onClick={reopen}>
          {busy ? "正在连接…" : tab.kind === "terminal" ? "新开终端" : "连接并打开"}
        </button>
        {durableHost && (
          <button type="button" className="nx-btn nx-btn-ghost" disabled={busy} onClick={openBackground}>
            查看后台会话
          </button>
        )}
      </div>
    </div>
  );
}

// A restored host workspace is not dialed until the user asks; its tabs share this connect prompt.
function RestoredWorkspacePlaceholder({ workspaceId }: { workspaceId: string }) {
  const workspace = useUi((state) => state.workspaces.find((item) => item.id === workspaceId));
  const [connecting, setConnecting] = useState(false);
  if (!workspace) return null;
  const durableHost = workspace.assetKind === "ssh" || workspace.assetKind === "device";
  const connect = () => {
    setConnecting(true);
    void reconnectRestoredWorkspace(workspaceId)
      .then((result) => {
        if (!result.ok && result.error) useUi.getState().pushToast("error", result.error);
      })
      .finally(() => setConnecting(false));
  };
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-neutral-400">
      <span>
        {workspace.deletedTarget
          ? `「${workspace.title}」的资产已删除，无法重新连接。`
          : `「${workspace.title}」尚未连接。连接后可新建终端${durableHost ? "，原有持久终端可在「后台会话」中接管" : ""}。`}
      </span>
      {!workspace.deletedTarget && (
        <button type="button" className="nx-btn nx-btn-outline" disabled={connecting} onClick={connect}>
          {connecting ? "正在连接…" : "连接"}
        </button>
      )}
    </div>
  );
}

// The few panels that list every session subscribe here, so other tabs do not re-render on session changes.
// A log tab opened in a host workspace starts with that host selected.
function MultiHostLogTailTab({ initialSelected }: { initialSelected?: string[] }) {
  const sessions = useSessionList();
  return (
    <Lazy>
      <MultiHostLogTail sessions={sessions} initialSelected={initialSelected} />
    </Lazy>
  );
}

function FleetHealthTab({ visible }: { visible: boolean }) {
  const sessions = useSessionList();
  return <Lazy><FleetHealthPanel sessions={sessions} visible={visible} /></Lazy>;
}

function ExecBatchTab() {
  const sessions = useSessionList();
  return <Lazy><ExecBatchPanel sessions={sessions} /></Lazy>;
}

// A database tab remounts its panel when the workspace runtime is reset.
function DbTab({ connId, kind, workspaceId }: { connId: string; kind: DbKind; workspaceId: string }) {
  const workspaceRuntimeEpoch = useUi((state) => state.workspaceRuntimeEpoch);
  return (
    <Lazy>
      <DbPanel key={workspaceRuntimeEpoch} connId={connId} kind={kind} workspaceId={workspaceId} />
    </Lazy>
  );
}

export const PaneForTab = memo(function PaneForTab({
  tab,
  active,
  workspaceId,
  onClose,
}: {
  tab: AppTab;
  active: boolean;
  workspaceId: string;
  onClose: (id: string) => Promise<void>;
}) {
  const restorePending = useUi((state) => state.workspaces.find((workspace) => workspace.id === workspaceId)?.restorePending);
  const close = () => void onClose(tab.id);
  if (restorePending) return <RestoredWorkspacePlaceholder workspaceId={workspaceId} />;
  if (tab.kind === "terminal" && tab.containerId && tab.restorePending) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-neutral-400">
        <span>容器终端不会自动重新打开，请从容器列表重新进入。</span>
        <button type="button" className="nx-btn nx-btn-outline" onClick={close}>关闭标签页</button>
      </div>
    );
  }
  if (tab.restorePending && (tab.kind === "terminal" || tab.kind === "files" || tab.kind === "log")) {
    return <RestoredTabPlaceholder tab={tab} workspaceId={workspaceId} />;
  }
  switch (tab.kind) {
    case "terminal":
      return tab.sessionId ? (
        <Lazy>
          <TerminalPane
            sessionId={tab.sessionId}
            title={tab.title}
            containerId={tab.containerId}
            storeTabId={tab.id}
            resumeTabId={tab.tabId}
            visible={active}
          />
        </Lazy>
      ) : (
        <EmptyState />
      );
    case "mount":
      return <Lazy><MountPanel sessionId={tab.sessionId} /></Lazy>;
    case "forward":
      return <Lazy><ForwardPanel sessionId={tab.sessionId} /></Lazy>;
    case "files":
      if (!tab.sessionId) return <EmptyState />;
      // A files tab is a file editor; a legacy browser tab (no path) has no view any more.
      if (!tab.path) return <EmptyState />;
      return <Lazy><FileEditor sessionId={tab.sessionId} path={tab.path} onClose={close} /></Lazy>;
    case "log":
      if (isMultiHostLogTabId(tab.id)) return <MultiHostLogTailTab initialSelected={tab.logSessionIds} />;
      if (isFleetHealthTabId(tab.id)) return <FleetHealthTab visible={active} />;
      if (!tab.sessionId || !tab.path) return <EmptyState />;
      return <Lazy><LogViewer sessionId={tab.sessionId} path={tab.path} onClose={close} /></Lazy>;
    case "docker":
      return tab.sessionId ? <Lazy><DockerPanel sessionId={tab.sessionId} visible={active} /></Lazy> : <EmptyState />;
    case "db":
      return tab.connId ? <DbTab connId={tab.connId} kind={tab.dbKind ?? "mysql"} workspaceId={workspaceId} /> : <EmptyState />;
    case "execBatch":
      return <ExecBatchTab />;
    case "settings":
      return <Lazy><SettingsView active={active} /></Lazy>;
    case "audit":
      return <Lazy><AuditView /></Lazy>;
    case "devices":
      return <LazyDevicesView />;
    case "host":
      return <Lazy><HostDetailView assetId={tab.assetId} visible={active} /></Lazy>;
    case "background":
      return (
        <Lazy>
          <BackgroundSessions
            visible={active}
            workspaceId={workspaceId}
            sessionId={tab.sessionId}
          />
        </Lazy>
      );
    case "hostHistory":
      return (
        <Lazy>
          <TranscriptHistoryPanel visible={active} workspaceId={workspaceId} assetId={tab.assetId} fixedHost />
        </Lazy>
      );
    case "history":
      return (
        <Lazy>
          <TranscriptHistoryPanel
            visible={active}
            workspaceId={workspaceId}
            assetId={tab.assetId}
          />
        </Lazy>
      );
    case "credentials":
      return <Lazy><CredentialsPanel credId={tab.credId} /></Lazy>;
    default:
      return <EmptyState />;
  }
});
