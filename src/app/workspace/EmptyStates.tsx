import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { connectAsset, useUi, openTerminalTab } from "../store";
import { formatBinding, useKeybindings } from "../keybindings";
import { useShell } from "../shell";
import { sessionApi } from "../../ipc/commands";
import { assetApi, type Asset } from "../../ipc/commands";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useConnectHistory } from "../../features/explorer/connectHistory";
import { relativeTime } from "../../features/fleet/format";
import { Lazy } from "../../ui/LazyBoundary";
import { AssetEditor } from "../lazyPanels";
import { IconCommand, IconPlus, IconSearch, IconTerminal, Logo, assetIcon } from "../../ui/icons";
import { assetKindName, recentConnections } from "./recentConnections";

export function WorkspaceEmpty({ onNew }: { onNew: () => void }) {
  return (
    <div className="flex h-full items-center justify-center bg-neutral-900 px-6">
      <div className="flex w-[300px] flex-col items-center gap-3.5 rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-6 py-7 text-center">
        <div className="nx-empty-icon">
          <IconTerminal size={18} />
        </div>
        <div>
          <div className="text-[13.5px] font-semibold tracking-tight text-neutral-100">暂无终端</div>
          <div className="mt-1 text-[11.5px] text-neutral-500">创建一个新的终端会话</div>
        </div>
        <button className="nx-btn nx-btn-primary" onClick={onNew}>
          <IconPlus size={14} />
          新建终端
        </button>
        <div className="nx-hint">在左侧栏的文件树中双击文件，可打开编辑器标签页。</div>
      </div>
    </div>
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
      <div className="nx-hint max-w-md text-center">
        双击左侧栏中的资产可打开其工作区（「当前设备」即本机）。主机工作区的左侧栏默认显示该主机的文件树，其中的容器、端口转发和日志按钮作用于当前主机；选择「资产」或「凭据」可临时切换侧栏。主机工作区不会自动新建终端，请选择「新建终端」。容器资产打开容器面板，MySQL / PostgreSQL / Redis 资产打开数据库工作台。
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
            <ol className="mt-2 list-decimal space-y-1 pl-4 text-[12px] text-neutral-400">
              <li>点击「新建资产」，填写主机地址与登录方式，支持 SSH、WinRM、RDP、VNC、Docker 和数据库。</li>
              <li>保存后会立即连接；之后双击侧栏中的资产即可再次打开。</li>
              <li>{mobile ? "点击「快速连接」输入 user@host 临时连接。" : "按快速连接快捷键输入 user@host 可临时连接，不必先保存。"}</li>
            </ol>
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

        <div className="nx-hint max-w-md text-center">
          双击左侧栏中的资产可打开其工作区。容器资产打开容器面板，MySQL / PostgreSQL / Redis 资产打开数据库工作台，RDP / VNC 资产打开远程桌面。
        </div>

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
