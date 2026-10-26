import { dbApi, sessionApi, vaultApi, type SessionInfo, type VaultStatus } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { workspaceViewport } from "../../features/terminal/workspaceLayout";
import { useConnectHistory } from "../../features/explorer/connectHistory";
import { connectWithHostKeyConfirm } from "../hostKeys";
import { formatQuickConnectTarget, type QuickConnectTarget } from "../quickConnectTarget";
import { DB_KIND_LABEL, dbKindOf, makePane, nextTabId, rebindWorkspaceSession } from "./model";
import {
  assetConnectionProblemKey,
  getWorkspaceProblemResetGeneration,
  latestConnectionProblem,
  quickConnectionProblemKey,
  selectConnectionProblem,
} from "./problems";
import { promptQuickConnectPassword } from "./quickConnect";
import { reconnectSessionAndWait } from "./reconnect";
import { openTerminalTab } from "./tabActions";
import type {
  AppTab,
  ConnectAssetInput,
  ConnectAssetOptions,
  ConnectOutcome,
  QuickWorkspaceTarget,
  Workspace,
  WorkspaceProblem,
  WorkspaceProblemConnection,
  WorkspaceProblemTarget,
} from "./types";
import { useUi } from "./useUi";
import { reconnectRestoredWorkspace } from "./workspaceRestore";

export async function ensureVaultUnlocked(reason: string, epoch?: number): Promise<boolean> {
  let st: VaultStatus;
  try {
    st = await vaultApi.status();
  } catch {
    return epoch === undefined || currentConnectionEpoch(epoch);
  }
  if (epoch !== undefined && !currentConnectionEpoch(epoch)) return false;
  if (!st.initialized || st.unlocked) return true;
  if (st.passwordless) {
    try {
      await vaultApi.unlock("");
      if (epoch !== undefined && !currentConnectionEpoch(epoch)) return false;
      return true;
    } catch (e) {
      if (epoch === undefined || currentConnectionEpoch(epoch)) useUi.getState().pushToast("error", `解锁失败：${describeError(e)}`);
      return false;
    }
  }
  const { promptText } = await import("../../ui/dialogs");
  if (epoch !== undefined && !currentConnectionEpoch(epoch)) return false;
  const pwd = await promptText(reason, "", { secret: true });
  if (pwd === null || (epoch !== undefined && !currentConnectionEpoch(epoch))) return false;
  try {
    await vaultApi.unlock(pwd);
    if (epoch !== undefined && !currentConnectionEpoch(epoch)) return false;
    return true;
  } catch (e) {
    if (epoch === undefined || currentConnectionEpoch(epoch)) useUi.getState().pushToast("error", `解锁失败：${describeError(e)}`);
    return false;
  }
}

export async function ensureVaultReadyFor(asset: { name: string; credId?: string | null }, epoch?: number): Promise<boolean> {
  if (!asset.credId) return true;
  return ensureVaultUnlocked(`连接「${asset.name}」需要使用凭据，请先输入保护密码：`, epoch);
}

function sidebarsOverlayWorkspace(): boolean {
  if (typeof window === "undefined") return false;
  return workspaceViewport(window.innerWidth, false).overlaySidebars;
}

export async function openConnectedAssetSession(
  info: SessionInfo,
  asset: { id: string; name: string; kind: string },
  options?: ConnectAssetOptions,
  quickTarget?: QuickWorkspaceTarget,
): Promise<void> {
  const { setSessions, sessions, workspaces } = useUi.getState();
  setSessions([...sessions.filter((s) => s.id !== info.id), info]);
  const restored = asset.id && (asset.kind === "ssh" || asset.kind === "device")
    ? workspaces.find((workspace) => workspace.kind === "session" && workspace.assetId === asset.id)
    : undefined;
  if (restored) {
    useUi.setState((state) => {
      const rebound = rebindWorkspaceSession(state.workspaces, restored.id, restored.sessionId, info.id);
      if (options?.preserveUserFocus) return { workspaces: rebound };
      return {
        activeWorkspaceId: restored.id,
        workspaces: rebound.map((workspace) => {
          if (workspace.id !== restored.id || asset.kind === "device" || workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal"))) return workspace;
          const panes = workspace.panes.length > 0 ? workspace.panes : [makePane()];
          const activePane = panes.find((pane) => pane.id === workspace.activePaneId) ?? panes[0];
          const tab: AppTab = {
            id: nextTabId(`term-${info.id}`), kind: "terminal", title: "终端 1", sessionId: info.id, closable: true,
          };
          return {
            ...workspace,
            activePaneId: activePane.id,
            panes: panes.map((pane) => pane.id === activePane.id
              ? { ...pane, tabs: [...pane.tabs, tab], activeTabId: tab.id }
              : pane),
          };
        }),
      };
    });
    return;
  }
  if (options?.preserveUserFocus && workspaces.length > 0) return;
  const pane = makePane();
  if (asset.kind !== "device") {
    const tab: AppTab = asset.kind === "docker"
      ? { id: nextTabId(`docker-${info.id}`), kind: "docker", title: "容器", sessionId: info.id, closable: true }
      : { id: nextTabId(`term-${info.id}`), kind: "terminal", title: "终端 1", sessionId: info.id, closable: true };
    pane.tabs = [tab];
    pane.activeTabId = tab.id;
  }
  const workspace: Workspace = {
    id: nextTabId("ws-session"), kind: "session", sessionId: info.id,
    title: info.name, assetId: asset.id, assetKind: asset.kind, quickTarget,
    panes: [pane], activePaneId: pane.id, closable: true,
  };
  const overlaySidebars = sidebarsOverlayWorkspace();
  useUi.setState((state) => ({
    workspaces: [...state.workspaces, workspace],
    activeWorkspaceId: workspace.id,
    leftMode: overlaySidebars ? state.leftMode : "files",
    leftOpen: overlaySidebars ? state.leftOpen : true,
    connectFocusRevision: state.connectFocusRevision + 1,
  }));
}

const inflightConnects = new Map<string, Promise<ConnectOutcome>>();
const connectingAssetCounts = new Map<string, number>();
const restoreWorkspaceIds = new WeakMap<Workspace, number>();
let restoreWorkspaceIdSeq = 0;

export function currentConnectionEpoch(epoch: number): boolean {
  return useUi.getState().workspaceRuntimeEpoch === epoch;
}

export async function discardStaleSession(session: SessionInfo): Promise<ConnectOutcome> {
  await sessionApi.disconnect(session.id).catch(() => undefined);
  return { ok: false, canceled: true };
}

async function discardStaleDatabase(connId: string): Promise<ConnectOutcome> {
  await dbApi.disconnect(connId).catch(() => undefined);
  return { ok: false, canceled: true };
}

function pendingWorkspaceKey(assetId: string): number | null {
  const workspace = useUi.getState().workspaces.find((item) => item.assetId === assetId && item.restorePending);
  if (!workspace) return null;
  let id = restoreWorkspaceIds.get(workspace);
  if (id === undefined) {
    id = ++restoreWorkspaceIdSeq;
    restoreWorkspaceIds.set(workspace, id);
  }
  return id;
}

function inflightAssetKey(assetId: string, epoch: number, options?: ConnectAssetOptions): string {
  return JSON.stringify([
    assetId,
    epoch,
    pendingWorkspaceKey(assetId),
    options?.silent === true,
    options?.preserveUserFocus === true,
  ]);
}

function setAssetConnecting(assetId: string, connecting: boolean, epoch: number): void {
  const key = JSON.stringify([epoch, assetId]);
  const current = connectingAssetCounts.get(key) ?? 0;
  const next = connecting ? current + 1 : Math.max(0, current - 1);
  if (next === 0) connectingAssetCounts.delete(key);
  else connectingAssetCounts.set(key, next);
  if (!currentConnectionEpoch(epoch)) return;
  useUi.setState((s) => ({
    connectingAssetIds:
      next > 0
        ? s.connectingAssetIds.includes(assetId)
          ? s.connectingAssetIds
          : [...s.connectingAssetIds, assetId]
        : s.connectingAssetIds.filter((id) => id !== assetId),
  }));
}

export function isAssetConnecting(assetId: string): boolean {
  return useUi.getState().connectingAssetIds.includes(assetId);
}

export function useConnectionProblem(
  connectionKey: string | null,
  fallbackToLatest = false,
): WorkspaceProblem | undefined {
  return useUi((state) => {
    if (connectionKey) {
      const problem = selectConnectionProblem(state, connectionKey);
      if (problem) return problem;
    }
    return fallbackToLatest ? latestConnectionProblem(state) : undefined;
  });
}

interface ConnectionAttemptContext {
  resetGeneration: number;
}

export function captureConnectionAttempt(): ConnectionAttemptContext {
  return { resetGeneration: getWorkspaceProblemResetGeneration() };
}

function settleConnectionOutcome(
  context: ConnectionAttemptContext,
  outcome: ConnectOutcome,
  operation: string,
  target: WorkspaceProblemTarget,
  connection: WorkspaceProblemConnection,
  workspaceId?: string,
): void {
  if (context.resetGeneration !== getWorkspaceProblemResetGeneration()) return;
  const connectionKey = connection.kind === "asset"
    ? assetConnectionProblemKey(connection.asset.id)
    : quickConnectionProblemKey(connection.target);
  if (outcome.ok) {
    useUi.getState().clearConnectionProblem(connectionKey);
    return;
  }
  if (outcome.canceled) return;
  const error = outcome.error ?? "连接失败";
  useUi.getState().recordConnectionProblem({
    connectionKey,
    workspaceId,
    resetGeneration: context.resetGeneration,
    createdAt: Date.now(),
    source: "connect",
    operation,
    target,
    summary: error,
    detail: error,
    canRetryRead: false,
    connection,
  });
}

function settleAssetConnectionOutcome(
  context: ConnectionAttemptContext,
  asset: ConnectAssetInput,
  outcome: ConnectOutcome,
  workspaceId?: string,
): void {
  const addressed = asset as ConnectAssetInput & { host?: unknown; port?: unknown };
  const host = typeof addressed.host === "string" ? addressed.host : "";
  const port = typeof addressed.port === "number" ? addressed.port : undefined;
  settleConnectionOutcome(
    context,
    outcome,
    "连接",
    {
      title: asset.name,
      identity: host ? `${asset.name} · ${host}${port ? `:${port}` : ""}` : asset.name,
      kind: asset.kind,
      assetId: asset.id,
    },
    { kind: "asset", asset: { ...asset } },
    workspaceId,
  );
}

export function settleQuickConnectionOutcome(
  context: ConnectionAttemptContext,
  target: QuickConnectTarget,
  outcome: ConnectOutcome,
  mode: "temporary" | "save" = "temporary",
  workspaceId?: string,
): void {
  const label = formatQuickConnectTarget(target);
  settleConnectionOutcome(
    context,
    outcome,
    mode === "save" ? "保存并连接" : "临时连接",
    { title: label, identity: label, kind: "ssh" },
    { kind: "quick", mode, target: { ...target }, label },
    workspaceId,
  );
}

export function settleWorkspaceConnectionOutcome(
  context: ConnectionAttemptContext,
  workspace: Pick<Workspace, "assetId" | "assetKind" | "title" | "quickTarget">,
  outcome: ConnectOutcome,
  workspaceId?: string,
): void {
  if (workspace.assetId) {
    settleAssetConnectionOutcome(
      context,
      { id: workspace.assetId, name: workspace.title, kind: workspace.assetKind ?? "ssh" },
      outcome,
      workspaceId,
    );
  } else if (workspace.quickTarget) {
    settleQuickConnectionOutcome(context, workspace.quickTarget, outcome, "temporary", workspaceId);
  }
}

export async function connectAsset(
  asset: ConnectAssetInput,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const key = inflightAssetKey(asset.id, epoch, options);
  const inflight = inflightConnects.get(key);
  if (inflight) return inflight;
  const context = captureConnectionAttempt();
  const promise = runConnectAsset(asset, options, epoch).finally(() => {
    if (inflightConnects.get(key) === promise) inflightConnects.delete(key);
    setAssetConnecting(asset.id, false, epoch);
    if (currentConnectionEpoch(epoch) && !isAssetConnecting(asset.id)) useUi.getState().setConnectProgress(asset.id, null);
  });
  inflightConnects.set(key, promise);
  setAssetConnecting(asset.id, true, epoch);
  const outcome = await promise;
  if (currentConnectionEpoch(epoch)) settleAssetConnectionOutcome(context, asset, outcome);
  return outcome;
}

const LIVE_SESSION_STATUSES = new Set(["connecting", "connected", "reconnecting"]);

async function runConnectAsset(
  asset: ConnectAssetInput,
  options?: ConnectAssetOptions,
  epoch = useUi.getState().workspaceRuntimeEpoch,
): Promise<ConnectOutcome> {
  if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
  const { pushToast } = useUi.getState();

  const existing = useUi.getState().workspaces.find((w) => w.assetId === asset.id);
  if (existing?.restorePending) return reconnectRestoredWorkspace(existing.id);
  const restoreDatabaseWorkspace =
    existing?.kind === "db" &&
    !existing.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "db"));
  if (existing) {
    const session = useUi.getState().sessions.find((s) => s.id === existing.sessionId);
    const live =
      !restoreDatabaseWorkspace &&
      (existing.kind === "db" ||
        (session !== undefined && LIVE_SESSION_STATUSES.has(session.status)));
    if (live) {
      const hasTerminal = existing.panes.some((p) => p.tabs.some((t) => t.kind === "terminal"));
      if (session && !hasTerminal) {
        void openTerminalTab(session);
      } else {
        useUi.getState().setActiveWorkspace(existing.id);
      }
      return { ok: true };
    }
    if (session) {
      if (!(await ensureVaultReadyFor(asset, epoch)) || !currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
      try {
        const fresh = await reconnectSessionAndWait(session);
        if (!currentConnectionEpoch(epoch)) return fresh ? discardStaleSession(fresh) : { ok: false, canceled: true };
        if (!fresh) {
          pushToast("info", "已取消连接");
          return { ok: false, canceled: true };
        }
        const { sessions, setSessions } = useUi.getState();
        setSessions([...sessions.filter((s) => s.id !== fresh.id), fresh]);
        useUi.getState().setActiveWorkspace(existing.id);
        useConnectHistory.getState().record(asset.id);
        return { ok: true };
      } catch (error) {
        if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
        if ((error as { code?: string } | null)?.code !== "not_found") {
          const message = describeError(error);
          pushToast("error", `连接失败：${message}`);
          return { ok: false, error: message };
        }
      }
    }
    if (!restoreDatabaseWorkspace && asset.kind !== "ssh" && asset.kind !== "device") {
      await useUi.getState().closeWorkspace(existing.id);
      if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
      if (useUi.getState().workspaces.some((w) => w.id === existing.id)) {
        return { ok: false, canceled: true };
      }
    }
  }

  const dbKind = dbKindOf(asset.kind);
  if (dbKind) {
    try {
      if (!(await ensureVaultReadyFor(asset, epoch)) || !currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
      const { connId } = await dbApi.connect(asset.id);
      if (!currentConnectionEpoch(epoch)) return discardStaleDatabase(connId);
      const title = `${asset.name} · ${DB_KIND_LABEL[dbKind]}`;
      const tab: AppTab = {
        id: nextTabId(`db-${connId}`),
        kind: "db",
        title: DB_KIND_LABEL[dbKind],
        connId,
        dbKind,
        closable: true,
      };
      if (restoreDatabaseWorkspace && existing) {
        useUi.setState((state) => {
          const runtime = state.workspaceRuntime[existing.id];
          return {
            activeWorkspaceId: existing.id,
            workspaces: state.workspaces.map((workspace) =>
              workspace.id === existing.id ? { ...workspace, connId, dbKind, title } : workspace,
            ),
            workspaceRuntime: runtime?.sqlDraft
              ? {
                  ...state.workspaceRuntime,
                  [existing.id]: {
                    ...runtime,
                    sqlDraft: { ...runtime.sqlDraft, connId },
                  },
                }
              : state.workspaceRuntime,
          };
        });
        useUi.getState().addTab(tab, existing.activePaneId);
      } else {
        const { addTab, ensureWorkspace } = useUi.getState();
        ensureWorkspace({ kind: "db", connId, dbKind, title, assetId: asset.id });
        addTab(tab);
      }
      useConnectHistory.getState().record(asset.id);
      return { ok: true };
    } catch (e) {
      if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
      if (!options?.silent) pushToast("error", describeError(e));
      return { ok: false, error: describeError(e) };
    }
  }

  try {
    if (!(await ensureVaultReadyFor(asset, epoch)) || !currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
    const info = await connectWithHostKeyConfirm(() => sessionApi.connect(asset.id), () => currentConnectionEpoch(epoch));
    if (!currentConnectionEpoch(epoch)) return info ? discardStaleSession(info) : { ok: false, canceled: true };
    if (!info) {
      pushToast("info", "已取消连接");
      return { ok: false, canceled: true };
    }
    await openConnectedAssetSession(info, asset, options);
    useConnectHistory.getState().record(asset.id);
    return { ok: true };
  } catch (e) {
    if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

// deviceAssetId is the pseudo asset id of a device session in the session system (matching the
// asset.ID of the backend session_connect_device); devices are not asset table records, so credential/host-key flows never apply.
export function deviceAssetId(deviceId: string): string {
  return `device/${deviceId}`;
}

// connectDevice opens the full workspace of a fleet device: the session is established via session_connect_device
// (the server enforces owner/revocation/terminal-switch authorization). Opening the workspace preserves its panes;
// a shell is created only when requested through the terminal action. Workspace dedup/reconnect mirrors connectAsset.
export async function connectDevice(
  device: { id: string; name: string },
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const assetId = deviceAssetId(device.id);
  const key = inflightAssetKey(assetId, epoch, options);
  const inflight = inflightConnects.get(key);
  if (inflight) return inflight;
  const context = captureConnectionAttempt();
  const promise = runConnectDevice(device, options, epoch).finally(() => {
    if (inflightConnects.get(key) === promise) inflightConnects.delete(key);
    setAssetConnecting(assetId, false, epoch);
  });
  inflightConnects.set(key, promise);
  setAssetConnecting(assetId, true, epoch);
  const outcome = await promise;
  if (currentConnectionEpoch(epoch)) {
    settleAssetConnectionOutcome(
      context,
      { id: assetId, name: device.name, kind: "device" },
      outcome,
    );
  }
  return outcome;
}

function activateDeviceWorkspace(workspaceId: string, options?: ConnectAssetOptions): void {
  if (options?.preserveUserFocus) return;
  const overlaySidebars = sidebarsOverlayWorkspace();
  useUi.setState((state) => ({
    activeWorkspaceId: workspaceId,
    leftMode: overlaySidebars ? state.leftMode : "files",
    leftOpen: overlaySidebars ? state.leftOpen : true,
    connectFocusRevision: state.connectFocusRevision + 1,
  }));
}

async function runConnectDevice(
  device: { id: string; name: string },
  options?: ConnectAssetOptions,
  epoch = useUi.getState().workspaceRuntimeEpoch,
): Promise<ConnectOutcome> {
  if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
  const { pushToast } = useUi.getState();
  const assetId = deviceAssetId(device.id);

  const existing = useUi.getState().workspaces.find((w) => w.assetId === assetId);
  if (existing?.restorePending) {
    const outcome = await reconnectRestoredWorkspace(existing.id);
    if (outcome.ok && currentConnectionEpoch(epoch)) activateDeviceWorkspace(existing.id, options);
    return outcome;
  }
  if (existing) {
    const session = useUi.getState().sessions.find((s) => s.id === existing.sessionId);
    const live = session !== undefined && LIVE_SESSION_STATUSES.has(session.status);
    if (live) {
      activateDeviceWorkspace(existing.id, options);
      return { ok: true };
    }
    if (session) {
      try {
        const fresh = await reconnectSessionAndWait(session);
        if (!currentConnectionEpoch(epoch)) return fresh ? discardStaleSession(fresh) : { ok: false, canceled: true };
        if (!fresh) {
          pushToast("info", "已取消连接");
          return { ok: false, canceled: true };
        }
        const { sessions, setSessions } = useUi.getState();
        setSessions([...sessions.filter((s) => s.id !== fresh.id), fresh]);
        activateDeviceWorkspace(existing.id, options);
        return { ok: true };
      } catch (error) {
        if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
        if ((error as { code?: string } | null)?.code !== "not_found") {
          const message = describeError(error);
          pushToast("error", `连接失败：${message}`);
          return { ok: false, error: message };
        }
      }
    }
  }

  try {
    const info = await sessionApi.connectDevice(device.id, device.name);
    if (!currentConnectionEpoch(epoch)) return discardStaleSession(info);
    await openConnectedAssetSession(info, { id: assetId, name: device.name, kind: "device" }, options);
    if (existing) activateDeviceWorkspace(existing.id, options);
    return { ok: true };
  } catch (e) {
    if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

// reconnectDeadHost establishes a fresh session for a workspace whose session was deleted on the server (the
// dead-session reconnect of the file panels and the terminal dead-tab overlay). A device workspace's assetId is the
// pseudo device/<deviceId> with no asset table record, so it reconnects through the same fleet device connect as the
// original open. Quick connections retain their non-secret target and request fresh credentials.
export async function reconnectDeadHost(
  ws: Pick<Workspace, "assetId" | "assetKind" | "title" | "quickTarget" | "deletedTarget">,
  options?: { isCurrent?: () => boolean },
): Promise<SessionInfo | null> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const context = captureConnectionAttempt();
  const isCurrent = () => currentConnectionEpoch(epoch) && (options?.isCurrent?.() ?? true);
  const finish = async (pending: Promise<SessionInfo | null>): Promise<SessionInfo | null> => {
    try {
      const session = await pending;
      if (isCurrent()) {
        if (session) settleWorkspaceConnectionOutcome(context, ws, { ok: true });
        return session;
      }
      if (session) await discardStaleSession(session);
      return null;
    } catch (error) {
      if (!isCurrent()) return null;
      settleWorkspaceConnectionOutcome(context, ws, { ok: false, error: describeError(error) });
      throw error;
    }
  };
  if (!isCurrent()) return null;
  if (ws.deletedTarget) throw new Error(`「${ws.title}」的资产已删除，请选择其他资产或关闭工作区`);
  if (ws.assetKind === "device" && ws.assetId) {
    return finish(sessionApi.connectDevice(ws.assetId.slice("device/".length), ws.title));
  }
  if (ws.assetId) return finish(connectWithHostKeyConfirm(() => sessionApi.connect(ws.assetId as string), isCurrent));
  if (ws.quickTarget) {
    const target = ws.quickTarget;
    const password = target.authKind === "password"
      ? await promptQuickConnectPassword(formatQuickConnectTarget(target))
      : "";
    if (!isCurrent()) return null;
    if (password === null) return null;
    if (target.authKind === "password" && !password) throw new Error("此快速连接需要重新输入密码");
    return finish(connectWithHostKeyConfirm(() => sessionApi.connectQuick({
      host: target.host,
      port: target.port,
      ...(target.username ? { username: target.username } : {}),
      authKind: target.authKind,
      ...(password ? { password } : {}),
    }), isCurrent));
  }
  if (ws.assetKind === "local") return finish(sessionApi.connectLocal());
  throw new Error(`「${ws.title}」缺少可恢复的主机目标，请从资产树重新连接`);
}
