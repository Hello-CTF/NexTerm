import { memo, useEffect, useState } from "react";
import {
  useUi,
  reconnectRestoredTab,
  reconnectRestoredWorkspace,
  type AppTab,
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
  FileBrowser,
  FileEditor,
  FleetHealthPanel,
  ForwardPanel,
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
  const reopen = () => {
    void reconnectRestoredTab(tab.id).then((result) => {
      if (!result.ok && result.error) useUi.getState().pushToast("error", result.error);
    });
  };
  const openBackground = () => {
    void reconnectRestoredWorkspace(workspaceId).then((result) => {
      const state = useUi.getState();
      if (!result.ok) {
        if (result.error) state.pushToast("error", result.error);
        return;
      }
      const workspace = state.workspaces.find((item) => item.id === workspaceId);
      if (!workspace?.sessionId) return;
      state.addTab({ id: `background-${workspaceId}`, kind: "background", title: "后台会话", sessionId: workspace.sessionId, closable: true });
    });
  };
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-neutral-400">
      <span>
        {tab.kind === "terminal"
          ? durableHost
            ? "原终端未连接。重新连接会新开一个 shell；若原终端是持久终端，可在「后台会话」接管。"
            : "原终端进程已结束，重新连接会新开一个 shell。"
          : "连接后重新打开此标签。"}
      </span>
      <div className="flex flex-wrap items-center justify-center gap-2">
        <button type="button" className="nx-btn nx-btn-outline" onClick={reopen}>
          {tab.kind === "terminal" ? "新开终端" : "连接并打开"}
        </button>
        {durableHost && (
          <button type="button" className="nx-btn nx-btn-ghost" onClick={openBackground}>
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
          : `「${workspace.title}」尚未连接。连接后会新开终端${durableHost ? "；之前的持久终端可在「后台会话」接管" : ""}。`}
      </span>
      {!workspace.deletedTarget && (
        <button type="button" className="nx-btn nx-btn-outline" disabled={connecting} onClick={connect}>
          {connecting ? "连接中…" : "连接"}
        </button>
      )}
    </div>
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
  const workspaceRuntimeEpoch = useUi((state) => state.workspaceRuntimeEpoch);
  const restorePending = useUi((state) => state.workspaces.find((workspace) => workspace.id === workspaceId)?.restorePending);
  const sessions = useUi((state) => state.sessions);
  const close = () => void onClose(tab.id);
  if (restorePending) return <RestoredWorkspacePlaceholder workspaceId={workspaceId} />;
  if (tab.kind === "terminal" && tab.containerId && tab.restorePending) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-neutral-400">
        <span>容器终端不会自动重新执行，请从容器列表重新打开。</span>
        <button type="button" className="nx-btn nx-btn-outline" onClick={close}>关闭标签</button>
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
      return (
        <Lazy>
          {tab.path ? (
            <FileEditor sessionId={tab.sessionId} path={tab.path} onClose={close} />
          ) : (
            <FileBrowser sessionId={tab.sessionId} />
          )}
        </Lazy>
      );
    case "log":
      if (isMultiHostLogTabId(tab.id)) return <Lazy><MultiHostLogTail sessions={sessions} /></Lazy>;
      if (isFleetHealthTabId(tab.id)) return <Lazy><FleetHealthPanel sessions={sessions} visible={active} /></Lazy>;
      if (!tab.sessionId || !tab.path) return <EmptyState />;
      return <Lazy><LogViewer sessionId={tab.sessionId} path={tab.path} onClose={close} /></Lazy>;
    case "docker":
      return tab.sessionId ? <Lazy><DockerPanel sessionId={tab.sessionId} visible={active} /></Lazy> : <EmptyState />;
    case "db":
      return tab.connId ? (
        <Lazy>
          <DbPanel
            key={workspaceRuntimeEpoch}
            connId={tab.connId}
            kind={tab.dbKind ?? "mysql"}
            workspaceId={workspaceId}
          />
        </Lazy>
      ) : (
        <EmptyState />
      );
    case "execBatch":
      return <Lazy><ExecBatchPanel sessions={sessions} /></Lazy>;
    case "settings":
      return <Lazy><SettingsView active={active} /></Lazy>;
    case "audit":
      return <Lazy><AuditView /></Lazy>;
    case "devices":
      return <LazyDevicesView />;
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
