import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { connectAsset, reopenRemoteDesktopTab, useUi, openTerminalTab } from "../store";
import {
  backgroundTabKey,
  backgroundTabName,
  backgroundTargets,
  listBackgroundTabs,
  openTerminalTabIds,
  resolveHostGate,
  takeOverBackgroundTab,
  useBackgroundSessionsAccess,
  visibleBackgroundTabs,
  workspaceSessionMatches,
  type BackgroundScope,
  type BackgroundTab,
} from "../../features/terminal/backgroundSessionsAccess";
import { getLocalHelperStatus, localDurableEnabled } from "../../features/terminal/localHelperStatus";
import { formatBinding, useKeybindings } from "../keybindings";
import { useShell } from "../shell";
import { sessionApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { assetApi, type Asset } from "../../ipc/commands";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useConnectHistory } from "../../features/explorer/connectHistory";
import { relativeTime } from "../../features/fleet/format";
import { Lazy } from "../../ui/LazyBoundary";
import { AssetEditor } from "../lazyPanels";
import { SshImportDialog } from "../../features/explorer/SshImportDialog";
import { IconCommand, IconDownload, IconPlus, IconSearch, IconTerminal, Logo, assetIcon } from "../../ui/icons";
import { assetKindName, recentConnections } from "./recentConnections";
import { t } from "../../i18n";

function EmptyCard({ title, subtitle, children }: { title: string; subtitle?: string; children?: ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center bg-neutral-900 px-6">
      <div className="flex w-[300px] flex-col items-center gap-3.5 rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-6 py-7 text-center">
        <div className="nx-empty-icon">
          <IconTerminal size={18} />
        </div>
        <div>
          <div className="text-[13.5px] font-semibold tracking-tight text-neutral-100">{title}</div>
          {subtitle && <div className="mt-1 text-[11.5px] text-neutral-500">{subtitle}</div>}
        </div>
        {children}
      </div>
    </div>
  );
}

// An empty pane of a remote desktop workspace: terminals do not belong here, so the only action reopens the desktop.
function RemoteWorkspaceEmpty({ workspaceId, paneId }: { workspaceId: string; paneId?: string }) {
  const workspace = useUi((s) => s.workspaces.find((item) => item.id === workspaceId));
  const [busy, setBusy] = useState(false);
  if (!workspace) return null;
  if (workspace.deletedTarget) {
    return <EmptyCard title={t("远程桌面已关闭")} subtitle={t("「{title}」的资产已删除，无法重新连接。", { title: workspace.title })} />;
  }
  const openElsewhere = workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "remote"));
  if (openElsewhere) return <EmptyCard title={t("此窗格暂无内容")} subtitle={t("远程桌面在其他窗格中打开。")} />;
  const reconnect = () => {
    setBusy(true);
    void reopenRemoteDesktopTab(workspaceId, paneId)
      .then((result) => {
        if (!result.ok && result.error) useUi.getState().pushToast("error", result.error);
      })
      .finally(() => setBusy(false));
  };
  return (
    <EmptyCard title={t("远程桌面已关闭")} subtitle={t("「{title}」的连接已断开。", { title: workspace.title })}>
      <button type="button" className="nx-btn nx-btn-primary" disabled={busy} onClick={reconnect}>
        {busy ? t("正在连接…") : t("重新连接")}
      </button>
    </EmptyCard>
  );
}

// An empty pane of a database workspace: a terminal here would open on this device, not on the database, so the only
// action reopens the database view in this pane.
function DatabaseWorkspaceEmpty({ workspaceId, paneId }: { workspaceId: string; paneId?: string }) {
  const workspace = useUi((s) => s.workspaces.find((item) => item.id === workspaceId));
  const [busy, setBusy] = useState(false);
  if (!workspace) return null;
  if (workspace.deletedTarget) {
    return <EmptyCard title={t("此窗格暂无内容")} subtitle={t("「{title}」的资产已删除，无法重新连接。", { title: workspace.title })} />;
  }
  const openElsewhere = workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "db"));
  const assetId = workspace.assetId;
  if (openElsewhere || !assetId) {
    return <EmptyCard title={t("此窗格暂无内容")} subtitle={t("从左侧栏或跳转面板打开所需的面板。")} />;
  }
  const reopen = () => {
    setBusy(true);
    // The reopened view lands in the workspace's focused pane, which is this one.
    if (paneId) useUi.getState().setActivePane(paneId, workspaceId);
    void assetApi
      .get(assetId)
      // connectAsset reports its own failures.
      .then((asset) => connectAsset(asset))
      .catch((error: unknown) => useUi.getState().pushToast("error", describeError(error)))
      .finally(() => setBusy(false));
  };
  return (
    <EmptyCard title={t("数据库已关闭")} subtitle={t("「{title}」的数据库面板已关闭。", { title: workspace.title })}>
      <button type="button" className="nx-btn nx-btn-primary" disabled={busy} onClick={reopen}>
        {busy ? t("正在连接…") : t("重新打开数据库")}
      </button>
    </EmptyCard>
  );
}

// workspaceId scopes the empty state to a pane of that workspace; without it this is the no-workspace fallback.
export function WorkspaceEmpty({ onNew, workspaceId, paneId }: { onNew: () => void; workspaceId?: string; paneId?: string }) {
  const kind = useUi((s) => (workspaceId ? s.workspaces.find((item) => item.id === workspaceId)?.kind : undefined));
  if (workspaceId && kind === "remote") return <RemoteWorkspaceEmpty workspaceId={workspaceId} paneId={paneId} />;
  if (workspaceId && kind === "db") return <DatabaseWorkspaceEmpty workspaceId={workspaceId} paneId={paneId} />;
  // A tools workspace holds several kinds of panels; the sidebar and the jump palette open the one wanted, and a
  // terminal button here would open a local terminal in another workspace.
  if (kind === "tools") return <EmptyCard title={t("此窗格暂无内容")} subtitle={t("从左侧栏或跳转面板打开所需的面板。")} />;
  return (
    <EmptyCard title={t("暂无终端")}>
      <button className="nx-btn nx-btn-primary" onClick={onNew}>
        <IconPlus size={14} />
        {t("新建终端")}
      </button>
      {workspaceId && kind === "session" && <HostBackgroundTerminals workspaceId={workspaceId} paneId={paneId} />}
    </EmptyCard>
  );
}

const BACKGROUND_LIST_LIMIT = 5;

// HostBackgroundTerminals lists the host's 持久终端 that run without a tab here, so opening a saved host (which opens
// no terminal by itself) offers taking one over next to starting a new one. It reads the same listing as the
// 后台会话 page, refreshed on the activity-bar count's interval: right after connecting, the host's durable helper may
// still be starting and list nothing yet.
const BACKGROUND_LIST_REFRESH_MS = 10_000;

function HostBackgroundTerminals({ workspaceId, paneId }: { workspaceId: string; paneId?: string }) {
  const workspace = useUi((s) => s.workspaces.find((item) => item.id === workspaceId));
  const sessionId = workspace?.sessionId;
  const status = useUi((s) => s.sessions.find((session) => session.id === sessionId)?.status);
  const access = useBackgroundSessionsAccess(workspace);
  const workspaceRef = useRef(workspace);
  workspaceRef.current = workspace;
  const [tabs, setTabs] = useState<BackgroundTab[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    setTabs([]);
    if (!access || !sessionId || status !== "connected") return;
    let cancelled = false;
    let inFlight = false;
    const refresh = () => {
      if (inFlight) return;
      const st = useUi.getState();
      const owner = workspaceRef.current ?? undefined;
      const scope: BackgroundScope = {
        sessions: st.sessions,
        workspaces: st.workspaces,
        verified: {},
        localDurable: localDurableEnabled(getLocalHelperStatus()),
      };
      const gate = resolveHostGate(owner, st.sessions.find((session) => session.id === sessionId), scope.verified);
      if (gate.needsReconnect) return;
      const targets = backgroundTargets(scope, { showAll: false, hostSessionId: sessionId, owner, remoteOwner: gate.remoteOwner });
      inFlight = true;
      void listBackgroundTabs(scope, targets, openTerminalTabIds(st.workspaces))
        .then(({ collected, liveFailed }) => {
          if (cancelled) return;
          // Nothing answered: keep what is shown rather than clearing it.
          if (liveFailed && collected.failedRemote.length === targets.length) return;
          setTabs(visibleBackgroundTabs(collected.list, scope, { hostSessionId: sessionId, showAll: false, blocked: false }));
        })
        .catch(() => undefined)
        .finally(() => {
          inFlight = false;
        });
    };
    refresh();
    const timer = window.setInterval(refresh, BACKGROUND_LIST_REFRESH_MS);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [access, sessionId, status, generation]);

  if (tabs.length === 0) return null;
  const takeOver = async (tab: BackgroundTab) => {
    const key = backgroundTabKey(tab);
    setBusy(key);
    try {
      const result = await takeOverBackgroundTab(tab, {
        // Leaving the workspace while the session list loads drops the takeover instead of pulling the user back.
        isCurrent: () => useUi.getState().activeWorkspaceId === workspaceId,
        guard: (session) => {
          // A listing up to one refresh old may name a session this workspace no longer holds.
          const owner = useUi.getState().workspaces.find((item) => item.id === workspaceId);
          const remoteOwner = resolveHostGate(owner, session, {}).remoteOwner;
          if (!owner || owner.sessionId !== session.id || (remoteOwner && !workspaceSessionMatches(owner, session, {}))) {
            throw new Error(t("该终端不属于当前主机，请重新连接后再接管"));
          }
          // The takeover adds the tab to the active pane right after this check: make that the pane that was used.
          if (paneId && owner.panes.some((pane) => pane.id === paneId)) useUi.getState().setActivePane(paneId, workspaceId);
        },
      });
      if (!result.ok && "error" in result) {
        useUi.getState().pushToast("error", t("接管失败：{error}", { error: result.error }));
        setGeneration((value) => value + 1);
      }
    } finally {
      setBusy((current) => (current === key ? null : current));
    }
  };
  const shown = tabs.slice(0, BACKGROUND_LIST_LIMIT);
  return (
    <section aria-label={t("后台运行的持久终端")} className="w-full border-t border-neutral-800/70 pt-3 text-left">
      <div className="mb-1 text-[11.5px] font-medium text-neutral-500">{t("后台运行的持久终端")}</div>
      <ul className="flex flex-col gap-1">
        {shown.map((tab) => {
          const key = backgroundTabKey(tab);
          const name = backgroundTabName(tab);
          return (
            <li key={key} className="flex items-center gap-2">
              <IconTerminal size={13} className="shrink-0 text-neutral-500" />
              <span className="min-w-0 flex-1 truncate text-[12px] text-neutral-200" title={name}>
                {name}
              </span>
              <button
                type="button"
                className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                aria-label={t("接管 {name}", { name })}
                disabled={busy !== null}
                onClick={() => void takeOver(tab)}
              >
                {t("接管")}
              </button>
            </li>
          );
        })}
      </ul>
      {tabs.length > shown.length && (
        <div className="mt-1 text-[11px] text-neutral-500">
          {t("另有 {count} 个，可在「后台会话」查看", { count: tabs.length - shown.length })}
        </div>
      )}
    </section>
  );
}

export function EmptyState({ onLocal, onPalette }: { onLocal?: () => void; onPalette?: () => void }) {
  const setSessions = useUi((s) => s.setSessions);
  const bindings = useKeybindings();
  const mobile = useShell() === "mobile";
  return (
    <div className="flex h-full flex-col items-center justify-center gap-5 bg-neutral-900 px-6">
      <Logo size={52} />
      <div className="text-center">
        <div className="text-[17px] font-semibold tracking-tight text-neutral-100">NexTerm</div>
        <div className="mt-1 text-xs text-neutral-500">
          {t("SSH、WinRM、文件、Docker、数据库和 AI，集中在一个工作台")}
        </div>
      </div>
      <div className="flex items-center gap-2">
        {!mobile && (
          <button
            className="nx-btn nx-btn-primary"
            onClick={() => {
              if (onLocal) {
                onLocal();
                return;
              }
              void sessionApi.connectLocal().then((s) => {
                setSessions([...useUi.getState().sessions.filter((x) => x.id !== s.id), s]);
                void openTerminalTab(s);
              });
            }}
          >
            <IconTerminal size={14} />
            {t("打开本机终端")}
          </button>
        )}
        {onPalette && (
          <button className="nx-btn nx-btn-outline" onClick={onPalette}>
            <IconCommand size={14} />
            {t("跳转面板")}
          </button>
        )}
      </div>
      <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[11px] text-neutral-500">
        {(
          [
            ["newTerminal", t("新建终端")],
            ["quickConnect", t("快速连接")],
            ["commandPalette", t("跳转面板")],
            ["toggleSidebar", t("左侧栏")],
            ["toggleAiSidebar", t("AI 侧栏")],
          ] as const
        ).map(([id, label]) => (
          <span key={id} className="inline-flex items-center gap-1">
            <span className="nx-kbd">{formatBinding(bindings[id])}</span>
            {label}
          </span>
        ))}
      </div>
    </div>
  );
}

function RecentRow({ asset, at, onOpen }: { asset: Asset; at: number; onOpen: () => void }) {
  const Icon = assetIcon(asset.kind, typeof asset.options?.icon === "string" ? asset.options.icon : undefined);
  const detail = asset.kind === "local" ? t("本机") : asset.host?.trim() || assetKindName(asset.kind);
  return (
    <button
      type="button"
      className="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left transition-colors hover:bg-neutral-800/70 focus-visible:bg-neutral-800/70"
      onClick={onOpen}
    >
      <Icon size={15} className="shrink-0 text-neutral-400" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[12.5px] text-neutral-100">{asset.name}</span>
        <span className="block truncate text-[11px] text-neutral-500">
          {assetKindName(asset.kind)} · {detail}
        </span>
      </span>
      <span className="shrink-0 text-[11px] text-neutral-500">{relativeTime(at, Date.now())}</span>
    </button>
  );
}

// WelcomePage is the empty workspace state: recent connections from the connect history, a quick connect entry, a new
// asset entry and, before any asset exists, a short first-run guide.
export function WelcomePage({
  onLocal,
  onQuickConnect,
  onPalette,
}: {
  onLocal?: () => void;
  onQuickConnect?: () => void;
  onPalette?: () => void;
}) {
  const setSessions = useUi((s) => s.setSessions);
  const bindings = useKeybindings();
  const mobile = useShell() === "mobile";
  const queryClient = useQueryClient();
  const assets = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list() });
  const history = useConnectHistory((s) => s.entries);
  const hiddenIds = useAssetVisibility((s) => s.hiddenIds);
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  // Importing reads ~/.ssh/config on this machine, which only the desktop app can do.
  const canImportSsh = !WEB && !mobile;

  const list = assets.data ?? [];
  const recent = recentConnections(list, history, { hiddenIds, mobile });
  const firstRun = assets.isSuccess && list.filter((asset) => asset.kind !== "local").length === 0;
  const openLocal = () => {
    if (onLocal) {
      onLocal();
      return;
    }
    void sessionApi.connectLocal().then((s) => {
      setSessions([...useUi.getState().sessions.filter((x) => x.id !== s.id), s]);
      void openTerminalTab(s);
    });
  };
  const onSaved = (asset?: Asset) => {
    setCreating(false);
    void queryClient.invalidateQueries({ queryKey: ["assets"] });
    void queryClient.invalidateQueries({ queryKey: ["asset-search"] });
    void queryClient.invalidateQueries({ queryKey: ["groups"] });
    void queryClient.invalidateQueries({ queryKey: ["credentials"] });
    if (asset) void connectAsset(asset);
  };

  return (
    <div className="flex h-full flex-col items-center gap-5 overflow-y-auto bg-neutral-900 px-6 py-8" data-testid="welcome-page">
      <div className="m-auto flex w-full max-w-md flex-col items-center gap-5">
        <Logo size={48} />
        <div className="text-center">
          <div className="text-[17px] font-semibold tracking-tight text-neutral-100">NexTerm</div>
          <div className="mt-1 text-xs text-neutral-500">{t("SSH、WinRM、远程桌面、文件、Docker、数据库和 AI，集中在一个工作台")}</div>
        </div>
        <div className="flex flex-wrap items-center justify-center gap-2">
          {onQuickConnect && (
            <button className="nx-btn nx-btn-primary" onClick={onQuickConnect}>
              <IconSearch size={14} />
              {t("快速连接")}
            </button>
          )}
          <button className={onQuickConnect ? "nx-btn nx-btn-outline" : "nx-btn nx-btn-primary"} onClick={() => setCreating(true)}>
            <IconPlus size={14} />
            {t("新建资产")}
          </button>
          {!mobile && (
            <button className="nx-btn nx-btn-outline" onClick={openLocal}>
              <IconTerminal size={14} />
              {t("打开本机终端")}
            </button>
          )}
          {onPalette && (
            <button className="nx-btn nx-btn-outline" onClick={onPalette}>
              <IconCommand size={14} />
              {t("跳转面板")}
            </button>
          )}
        </div>

        {firstRun ? (
          <section aria-label={t("开始使用")} className="w-full rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-4 py-3.5">
            <div className="text-[12.5px] font-semibold text-neutral-100">{t("开始使用")}</div>
            <p className="mt-2 text-[12px] text-neutral-400">
              {onQuickConnect && !WEB ? t("新建资产后立即连接；临时连接可用「快速连接」输入 user@host。") : t("新建资产后立即连接。")}
            </p>
            {canImportSsh && (
              <div className="mt-3 flex flex-col items-start gap-1.5">
                <button className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => setImporting(true)}>
                  <IconDownload size={13} />
                  {t("从 SSH 配置导入")}
                </button>
                <span className="text-[11px] text-neutral-500">{t("把 ~/.ssh/config 里的主机导入为资产。")}</span>
              </div>
            )}
          </section>
        ) : (
          <section aria-label={t("最近连接")} className="w-full">
            <div className="mb-1 px-2.5 text-[11.5px] font-medium text-neutral-500">{t("最近连接")}</div>
            {assets.isPending ? (
              <div className="nx-hint px-2.5 py-2" role="status">
                {t("正在加载…")}
              </div>
            ) : assets.isError ? (
              <div className="nx-hint px-2.5 py-2" role="status">
                {t("无法加载资产列表，最近连接暂不可用。")}
              </div>
            ) : recent.length === 0 ? (
              <div className="nx-hint px-2.5 py-2">{t("还没有连接记录。连接过的主机会显示在这里。")}</div>
            ) : (
              <ul className="flex flex-col">
                {recent.map(({ asset, at }) => (
                  <li key={asset.id}>
                    <RecentRow asset={asset} at={at} onOpen={() => void connectAsset(asset)} />
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}

        {!mobile && (
          <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[11px] text-neutral-500">
            {(
              [
                ["quickConnect", t("快速连接")],
                ["newTerminal", t("新建终端")],
                ["commandPalette", t("跳转面板")],
                ["toggleSidebar", t("左侧栏")],
              ] as const
            ).map(([id, label]) => (
              <span key={id} className="inline-flex items-center gap-1">
                <span className="nx-kbd">{formatBinding(bindings[id])}</span>
                {label}
              </span>
            ))}
          </div>
        )}
      </div>
      {creating && (
        <Lazy fallback="none" overlay>
          <AssetEditor kind="asset" onClose={() => setCreating(false)} onSaved={onSaved} />
        </Lazy>
      )}
      {importing && <SshImportDialog onClose={() => setImporting(false)} />}
    </div>
  );
}
