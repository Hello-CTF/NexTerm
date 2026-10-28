import { useCallback, useEffect, useMemo, useState } from "react";
import {
  useUi,
  openTerminalTab,
  nextTabId,
  connectAsset,
  dbKindOf,
  isSessionReconnectPending,
  reconnectDeadHost,
  reconnectWorkspaceSession,
  type Workspace,
} from "../store";
import { assetApi, sessionApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { showSettingsSection } from "../../features/settings/settingsNavigation";
import { createMultiHostLogTabId } from "../../features/logs/logTailTab";
import { startCwdTracking } from "../../features/files/cwdFollow";
import { isMobileShell } from "./shellMode";
import { hideMobileHome } from "./mobileHome";

function isSessionAlive(status: string): boolean {
  return status === "connected" || status === "connecting" || status === "reconnecting";
}

export interface WorkspaceActionsInput {
  ws: Workspace | null | undefined;
  backgroundAvailable: boolean;
  openDatabasePicker: () => void;
}

// useWorkspaceActions holds the commands the rail, header, keyboard shortcuts and empty states share.
export function useWorkspaceActions({ ws, backgroundAvailable, openDatabasePicker }: WorkspaceActionsInput) {
  const setSessions = useUi((s) => s.setSessions);
  const pushToast = useUi((s) => s.pushToast);
  const [vaultProtectionSeq, setVaultProtectionSeq] = useState(0);
  const activeSessionId = ws?.kind === "session" ? ws.sessionId : undefined;

  const needSession = useCallback(() => {
    pushToast("info", "先连接一台主机（双击左侧资产）");
  }, [pushToast]);

  const openLocalTerminal = useCallback(async () => {
    if (isMobileShell()) {
      pushToast("info", "移动端不支持本地终端，请双击资产连接远程主机");
      return;
    }
    const epoch = useUi.getState().workspaceRuntimeEpoch;
    try {
      const s = await sessionApi.connectLocal();
      if (useUi.getState().workspaceRuntimeEpoch !== epoch) return;
      const list = useUi.getState().sessions;
      setSessions([...list.filter((x) => x.id !== s.id), s]);
      await openTerminalTab(s);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  }, [pushToast, setSessions]);

  useEffect(() => startCwdTracking(), []);

  // openLogs opens a log follow tab; in a host workspace that host starts selected.
  const openLogs = useCallback(() => {
    useUi.getState().addTab({ id: createMultiHostLogTabId(), kind: "log", title: "日志", closable: true });
  }, []);

  const openMount = useCallback(() => {
    if (!activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`mount-${activeSessionId}`),
      kind: "mount",
      title: "磁盘挂载",
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  const openForward = useCallback(() => {
    if (!activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`forward-${activeSessionId}`),
      kind: "forward",
      title: "端口转发",
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  const openDocker = useCallback(() => {
    if (!activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`docker-${activeSessionId}`),
      kind: "docker",
      title: "容器",
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  const openDatabase = useCallback(async () => {
    const epoch = useUi.getState().workspaceRuntimeEpoch;
    try {
      const assets = (await assetApi.list()).filter((asset) => dbKindOf(asset.kind) !== null);
      if (useUi.getState().workspaceRuntimeEpoch !== epoch) return;
      if (assets.length === 0) {
        pushToast("info", "还没有数据库资产，先在资产树里新建一个");
        return;
      }
      if (assets.length === 1) {
        await connectAsset(assets[0]);
        return;
      }
      openDatabasePicker();
    } catch (e) {
      if (useUi.getState().workspaceRuntimeEpoch === epoch) pushToast("error", describeError(e));
    }
  }, [pushToast, openDatabasePicker]);

  const openAudit = useCallback(() => {
    useUi.getState().addTab({
      id: "audit",
      kind: "audit",
      title: "审计日志",
      closable: true,
    });
  }, []);

  const openDevices = useCallback(() => {
    useUi.getState().addTab({
      id: "devices",
      kind: "devices",
      title: "设备管理",
      closable: true,
    });
  }, []);

  const openSettings = useCallback(() => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
  }, []);
  const openSyncSettings = useCallback(() => {
    openSettings();
    window.location.hash = "#settings-account";
    showSettingsSection("settings-account");
  }, [openSettings]);

  useEffect(() => {
    if (window.location.hash === "#settings-account") openSettings();
  }, [openSettings]);

  // The session list auth guard (drop late responses / clear on logout) lives in the store's resyncSessions/resetSessions.
  // Vault state is cleared and refetched by resetQueries on the shared vault-status query; nothing extra is maintained here.
  const openVaultProtection = useCallback(() => {
    openSettings();
    setVaultProtectionSeq((n) => n + 1);
  }, [openSettings]);

  useEffect(() => {
    if (vaultProtectionSeq === 0) return;
    const wsId = useUi.getState().activeWorkspaceId;
    if (!wsId) return;
    for (const panel of document.querySelectorAll<HTMLElement>(
      `#nx-ws-panel-${CSS.escape(wsId)} [data-tab-content]:not(.hidden) [role='tabpanel']`,
    )) {
      const card = [...panel.querySelectorAll<HTMLElement>(".nx-card-title")]
        .find((el) => el.textContent?.trim() === "凭据保护")
        ?.closest<HTMLElement>(".nx-card");
      if (!card) continue;
      card.scrollIntoView({ block: "start" });
      break;
    }
  }, [vaultProtectionSeq]);

  const openBackground = useCallback(() => {
    if (ws?.kind !== "session" || !ws.sessionId) return needSession();
    if (!backgroundAvailable) return;
    useUi.getState().addTab({
      id: `background-${ws.id}`,
      kind: "background",
      title: "后台会话",
      sessionId: ws.sessionId,
      closable: true,
    });
  }, [ws?.id, ws?.kind, ws?.sessionId, backgroundAvailable, needSession]);

  const openHistory = useCallback(() => {
    useUi
      .getState()
      .addTab({ id: "history", kind: "history", title: "终端历史", closable: true });
  }, []);

  const openNewTerminal = useCallback(async () => {
    const epoch = useUi.getState().workspaceRuntimeEpoch;
    const workspaceId = ws?.id;
    const workspaceAssetId = ws?.assetId;
    const isCurrent = () => {
      const state = useUi.getState();
      return state.workspaceRuntimeEpoch === epoch &&
        (!workspaceId || state.workspaces.some((workspace) => workspace.id === workspaceId && workspace.assetId === workspaceAssetId));
    };
    const sid = ws?.sessionId;
    const s = sid ? useUi.getState().sessions.find((x) => x.id === sid) : undefined;
    if (s && ws) {
      if (isSessionReconnectPending(s.id)) {
        pushToast("info", "正在重连，连接成功后自动新建终端");
        return;
      }
      if (isSessionAlive(s.status)) {
        void openTerminalTab(s);
        return;
      }
      pushToast("info", "正在重连，连接成功后自动新建终端");
      try {
        const fresh = await reconnectWorkspaceSession(ws, s, { isCurrent });
        if (!isCurrent()) return;
        if (!fresh) {
          pushToast("info", "已取消重连");
          return;
        }
        const list = useUi.getState().sessions;
        setSessions([...list.filter((x) => x.id !== fresh.id), fresh]);
        await openTerminalTab(fresh);
      } catch (e) {
        if (isCurrent()) pushToast("error", `重新连接失败：${describeError(e)}`);
      }
      return;
    }
    if (ws?.assetId && ws.kind !== "db") {
      try {
        const fresh = await reconnectDeadHost(ws, { isCurrent });
        if (!isCurrent()) return;
        if (!fresh) {
          pushToast("info", "已取消重连");
          return;
        }
        const list = useUi.getState().sessions;
        setSessions([...list.filter((x) => x.id !== fresh.id), fresh]);
        await openTerminalTab(fresh);
      } catch (e) {
        if (isCurrent()) pushToast("error", `重新连接失败：${describeError(e)}`);
      }
      return;
    }
    if (isMobileShell()) {
      pushToast("info", "移动端不支持本地终端，请双击左侧资产连接远程主机");
      return;
    }
    void openLocalTerminal();
  }, [ws, setSessions, openLocalTerminal, pushToast]);

  const closeAllWorkspaces = useCallback(async () => {
    const state = useUi.getState();
    const activeId = state.activeWorkspaceId;
    const ids = state.workspaces
      .filter((workspace) => workspace.closable && workspace.id !== activeId)
      .map((workspace) => workspace.id);
    const active = state.workspaces.find((workspace) => workspace.id === activeId);
    if (active?.closable) ids.push(active.id);
    for (const id of ids) {
      if (!(await useUi.getState().closeWorkspace(id))) break;
    }
  }, []);

  // Each command opens something in the workspaces, so on the mobile shell it leaves the home first.
  const opening = useMemo(() => {
    const leaving = <A extends unknown[], R>(action: (...args: A) => R) => (...args: A): R => {
      hideMobileHome();
      return action(...args);
    };
    return {
      openLocalTerminal: leaving(openLocalTerminal),
      openLogs: leaving(openLogs),
      openMount: leaving(openMount),
      openForward: leaving(openForward),
      openDocker: leaving(openDocker),
      openDatabase: leaving(openDatabase),
      openAudit: leaving(openAudit),
      openDevices: leaving(openDevices),
      openSettings: leaving(openSettings),
      openSyncSettings: leaving(openSyncSettings),
      openVaultProtection: leaving(openVaultProtection),
      openBackground: leaving(openBackground),
      openHistory: leaving(openHistory),
      openNewTerminal: leaving(openNewTerminal),
    };
  }, [openLocalTerminal, openLogs, openMount, openForward, openDocker, openDatabase, openAudit, openDevices,
    openSettings, openSyncSettings, openVaultProtection, openBackground, openHistory, openNewTerminal]);

  return {
    needSession,
    ...opening,
    closeAllWorkspaces,
  };
}

export type WorkspaceActions = ReturnType<typeof useWorkspaceActions>;
