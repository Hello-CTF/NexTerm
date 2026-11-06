import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { connectAsset, reopenRemoteDesktopTab, useUi, openTerminalTab } from "../store";
import { formatBinding, useKeybindings } from "../keybindings";
import { useShell } from "../shell";
import { sessionApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { assetApi, type Asset } from "../../ipc/commands";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useConnectHistory } from "../../features/explorer/connectHistory";
import { relativeTime } from "../../features/fleet/format";
import { Lazy } from "../../ui/LazyBoundary";
import { AssetEditor } from "../lazyPanels";
import { IconCommand, IconPlus, IconSearch, IconTerminal, Logo, assetIcon } from "../../ui/icons";
import { assetKindName, recentConnections } from "./recentConnections";

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
    return <EmptyCard title="远程桌面已关闭" subtitle={`「${workspace.title}」的资产已删除，无法重新连接。`} />;
  }
  const openElsewhere = workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "remote"));
  if (openElsewhere) return <EmptyCard title="此窗格暂无内容" subtitle="远程桌面在其他窗格中打开。" />;
  const reconnect = () => {
    setBusy(true);
    void reopenRemoteDesktopTab(workspaceId, paneId)
      .then((result) => {
        if (!result.ok && result.error) useUi.getState().pushToast("error", result.error);
      })
      .finally(() => setBusy(false));
  };
  return (
    <EmptyCard title="远程桌面已关闭" subtitle={`「${workspace.title}」的连接已断开。`}>
      <button type="button" className="nx-btn nx-btn-primary" disabled={busy} onClick={reconnect}>
        {busy ? "正在连接…" : "重新连接"}
      </button>
    </EmptyCard>
  );
}

// workspaceId scopes the empty state to a pane of that workspace; without it this is the no-workspace fallback.
export function WorkspaceEmpty({ onNew, workspaceId, paneId }: { onNew: () => void; workspaceId?: string; paneId?: string }) {
  const kind = useUi((s) => (workspaceId ? s.workspaces.find((item) => item.id === workspaceId)?.kind : undefined));
  if (workspaceId && kind === "remote") return <RemoteWorkspaceEmpty workspaceId={workspaceId} paneId={paneId} />;
  if (kind === "db" || kind === "tools") {
    return (
      <EmptyCard title="此窗格暂无内容" subtitle="从左侧栏或跳转面板打开所需的面板。">
        <button className="nx-btn nx-btn-primary" onClick={onNew}>
          <IconPlus size={14} />
          新建终端
        </button>
      </EmptyCard>
    );
  }
  return (
    <EmptyCard title="暂无终端">
      <button className="nx-btn nx-btn-primary" onClick={onNew}>
        <IconPlus size={14} />
        新建终端
      </button>
    </EmptyCard>
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
          SSH、WinRM、文件、Docker、数据库和 AI，集中在一个工作台
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
            打开本地终端
          </button>
        )}
        {onPalette && (
          <button className="nx-btn nx-btn-outline" onClick={onPalette}>
            <IconCommand size={14} />
            跳转面板
          </button>
        )}
      </div>
      <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[11px] text-neutral-500">
        {(
          [
            ["newTerminal", "新建终端"],
            ["quickConnect", "快速连接"],
            ["commandPalette", "跳转面板"],
            ["toggleSidebar", "左侧栏"],
            ["toggleAiSidebar", "AI 侧栏"],
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
  const detail = asset.kind === "local" ? "本机" : asset.host?.trim() || assetKindName(asset.kind);
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
          <div className="mt-1 text-xs text-neutral-500">SSH、WinRM、远程桌面、文件、Docker、数据库和 AI，集中在一个工作台</div>
        </div>
        <div className="flex flex-wrap items-center justify-center gap-2">
          {onQuickConnect && (
            <button className="nx-btn nx-btn-primary" onClick={onQuickConnect}>
              <IconSearch size={14} />
              快速连接
            </button>
          )}
          <button className={onQuickConnect ? "nx-btn nx-btn-outline" : "nx-btn nx-btn-primary"} onClick={() => setCreating(true)}>
            <IconPlus size={14} />
            新建资产
          </button>
          {!mobile && (
            <button className="nx-btn nx-btn-outline" onClick={openLocal}>
              <IconTerminal size={14} />
              打开本地终端
            </button>
          )}
          {onPalette && (
            <button className="nx-btn nx-btn-outline" onClick={onPalette}>
              <IconCommand size={14} />
              跳转面板
            </button>
          )}
        </div>

        {firstRun ? (
          <section aria-label="开始使用" className="w-full rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-4 py-3.5">
            <div className="text-[12.5px] font-semibold text-neutral-100">开始使用</div>
            <p className="mt-2 text-[12px] text-neutral-400">
              {onQuickConnect && !WEB ? "新建资产后立即连接；临时连接可用「快速连接」输入 user@host。" : "新建资产后立即连接。"}
            </p>
          </section>
        ) : (
          <section aria-label="最近连接" className="w-full">
            <div className="mb-1 px-2.5 text-[11.5px] font-medium text-neutral-500">最近连接</div>
            {assets.isPending ? (
              <div className="nx-hint px-2.5 py-2" role="status">
                正在加载…
              </div>
            ) : assets.isError ? (
              <div className="nx-hint px-2.5 py-2" role="status">
                无法加载资产列表，最近连接暂不可用。
              </div>
            ) : recent.length === 0 ? (
              <div className="nx-hint px-2.5 py-2">还没有连接记录。连接过的主机会显示在这里。</div>
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
                ["quickConnect", "快速连接"],
                ["newTerminal", "新建终端"],
                ["commandPalette", "跳转面板"],
                ["toggleSidebar", "左侧栏"],
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
    </div>
  );
}
