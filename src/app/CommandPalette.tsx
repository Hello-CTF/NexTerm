import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useUi, connectAsset, nextTabId, openTerminalTab, requestKillTab } from "./store";
import { formatBinding, useKeybindings, type KeybindingActionId } from "./keybindings";
import { assetApi, dbApi, sessionApi, type Asset } from "../ipc/commands";
import { describeError } from "../ui/errorText";
import { DEMO } from "../demo";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../ui/DialogHost";
import { splitAllowedForHeight } from "../features/terminal/workspaceLayout";
import { insertSnippet } from "../features/explorer/snippetInsert";
import { cloneAsset } from "../features/explorer/assetClone";
import { useAssetVisibility } from "../features/explorer/assetVisibility";
import {
  probeAssetReachability,
  useAssetReachability,
  type ReachabilityEntry,
} from "../features/explorer/assetReachability";
import { ReachabilityDot } from "../features/explorer/ReachabilityDot";
import {
  IconActivity,
  IconClock,
  IconCommand,
  IconCopy,
  IconDatabase,
  IconEdit,
  IconEye,
  IconEyeOff,
  IconFolderOpen,
  IconHistory,
  IconKey,
  IconMonitor,
  IconNetwork,
  IconPlug,
  IconSearch,
  IconSettings,
  IconSplitH,
  IconTerminal,
  IconZap,
  assetIcon,
} from "../ui/icons";

interface Action {
  id: string;
  label: string;
  hint?: string;
  icon: typeof IconTerminal;
  dot?: ReachabilityEntry;
  run: () => void;
}

function snippetHint(body: string): string {
  const first = body.split(/\r?\n/, 1)[0]?.trim() ?? "";
  return first.length > 36 ? `${first.slice(0, 36)}…` : first;
}

export function CommandPalette({
  onClose,
  onOpenFiles,
  onEditAsset,
  onQuickConnect,
}: {
  onClose: () => void;
  onOpenFiles?: () => void;
  onEditAsset?: (asset: Asset) => void;
  onQuickConnect?: () => void;
}) {
  const qc = useQueryClient();
  const { sessions, setSessions, addTab, pushToast, themeMode, setThemeMode, connectingAssetIds } =
    useUi();
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const snippetsQuery = useQuery({ queryKey: ["snippets"], queryFn: () => assetApi.snippetList(), retry: false });
  const assets = assetsQuery.data ?? [];
  const snippets = snippetsQuery.data ?? [];
  const assetError = assetsQuery.isError ? `资产列表加载失败：${describeError(assetsQuery.error)}` : "";
  const snippetError = snippetsQuery.isError ? `片段列表加载失败：${describeError(snippetsQuery.error)}` : "";
  const { hiddenIds, showHidden } = useAssetVisibility();
  const reachEntries = useAssetReachability((s) => s.entries);
  const probedRef = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const closedRef = useRef(false);
  const listId = useId();
  const titleId = useId();
  const helpId = useId();
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => inputRef.current });
  const bindings = useKeybindings();
  const keyHint = (id: KeybindingActionId, description?: string): string | undefined => {
    const binding = bindings[id];
    if (!binding) return description;
    return description ? `${formatBinding(binding)} · ${description}` : formatBinding(binding);
  };

  useEffect(() => {
    if (probedRef.current || assets.length === 0) return;
    probedRef.current = true;
    const ids = assets
      .filter((a) => Boolean(a.host) && (showHidden || !hiddenIds.includes(a.id)))
      .slice(0, 16)
      .map((a) => a.id);
    if (ids.length > 0) void useAssetReachability.getState().probe(ids);
  }, [assets, hiddenIds, showHidden]);

  const runProbeOne = useCallback(
    async (asset: Asset) => {
      if (DEMO) {
        pushToast("info", "演示模式不发起真实探测");
        return;
      }
      if (!asset.host) {
        pushToast("info", `「${asset.name}」是本机资产，无需探测`);
        return;
      }
      const entry = await probeAssetReachability(asset.id);
      if (!entry || entry.state === "checking") {
        pushToast("error", `「${asset.name}」探测失败，请稍后重试`);
        return;
      }
      if (entry.state === "reachable") {
        pushToast("success", `「${asset.name}」可达 · ${entry.durationMs ?? 0}ms`);
      } else {
        pushToast("error", `「${asset.name}」不可达：${entry.error ?? "未知原因"}`);
      }
    },
    [pushToast],
  );

  const runClone = async (asset: Asset) => {
    try {
      const created = await cloneAsset(asset);
      if (!created) return;
      await qc.invalidateQueries({ queryKey: ["assets"] });
      pushToast("success", `已克隆为「${created.name}」`);
    } catch (e) {
      pushToast("error", `克隆失败：${describeError(e)}`);
    }
  };

  const hideAsset = (asset: Asset) => {
    useAssetVisibility.getState().hide(asset.id);
    pushToast("info", `已隐藏「${asset.name}」— 资产树眼睛按钮可找回`);
  };

  const unhideAsset = (asset: Asset) => {
    useAssetVisibility.getState().unhide(asset.id);
    pushToast("info", `已取消隐藏「${asset.name}」`);
  };

  const actions = useMemo<Action[]>(() => {
    const list: Action[] = [
      {
        id: "quick-connect",
        label: "快速连接…",
        hint: keyHint("quickConnect", "最近使用优先"),
        icon: IconZap,
        run: () => onQuickConnect?.(),
      },
      {
        id: "local-terminal",
        label: "打开本地终端",
        hint: keyHint("newTerminal", "当前设备"),
        icon: IconTerminal,
        run: () => {
          void sessionApi
            .connectLocal()
            .then((session) => {
              setSessions([...sessions.filter((item) => item.id !== session.id), session]);
              return openTerminalTab(session);
            })
            .catch((error) => pushToast("error", describeError(error)));
        },
      },
      {
        id: "database",
        label: "打开数据库工作台",
        hint: "MySQL / Redis",
        icon: IconDatabase,
        run: () => {
          const asset = assets.find((item) => item.kind === "mysql") ?? assets.find((item) => item.kind === "redis");
          if (!asset) {
            pushToast("info", "还没有数据库资产，先在资产树里新建一个");
            return;
          }
          void dbApi
            .connect(asset.id)
            .then(({ connId }) => {
              const kind = asset.kind === "redis" ? "redis" : "mysql";
              addTab({
                id: `db-${connId}`,
                kind: "db",
                title: `${asset.name} · ${kind === "mysql" ? "SQL" : "Redis"}`,
                connId,
                dbKind: kind,
                closable: true,
              });
            })
            .catch((error) => pushToast("error", describeError(error)));
        },
      },
      {
        id: "split-pane",
        label: "上下分屏 / 取消分屏",
        hint: keyHint("toggleSplit"),
        icon: IconSplitH,
        run: () => {
          const current = useUi.getState();
          const workspace = current.workspaces.find((item) => item.id === current.activeWorkspaceId);
          if (!workspace) {
            pushToast("info", "先连接一台主机（双击左侧资产）");
            return;
          }
          if (workspace.panes.length > 1) void current.unsplitWorkspace(workspace.panes[1].id, workspace.id);
          else if (splitAllowedForHeight(window.innerHeight)) current.splitWorkspace(workspace.id);
          else pushToast("info", "窗口高度不足，无法上下分屏");
        },
      },
      {
        id: "wide-files",
        label: "打开宽幅文件浏览器",
        hint: "含体积 / 修改时间列",
        icon: IconFolderOpen,
        run: () => onOpenFiles?.(),
      },
      {
        id: "port-forward",
        label: "打开端口转发",
        hint: "本地 / SOCKS5",
        icon: IconNetwork,
        run: () =>
          addTab({
            id: nextTabId("forward-cmd"),
            kind: "forward",
            title: "端口转发",
            closable: true,
          }),
      },
      {
        id: "settings",
        label: "打开设置",
        hint: "AI / 凭据库",
        icon: IconSettings,
        run: () => addTab({ id: "settings", kind: "settings", title: "设置", closable: true }),
      },
      {
        id: "background",
        label: "打开后台会话",
        hint: "转入后台的终端进程",
        icon: IconActivity,
        run: () => addTab({ id: "background", kind: "background", title: "后台会话", closable: true }),
      },
      {
        id: "history",
        label: "打开终端历史",
        hint: "回看和搜索终端输出",
        icon: IconClock,
        run: () => addTab({ id: "history", kind: "history", title: "终端历史", closable: true }),
      },
      {
        id: "devices",
        label: "打开设备管理",
        hint: "装了 agent 的设备",
        icon: IconMonitor,
        run: () => addTab({ id: "devices", kind: "devices", title: "设备管理", closable: true }),
      },
      {
        id: "credentials-view",
        label: "打开凭据视图",
        hint: "集中查看已保存凭据",
        icon: IconKey,
        run: () =>
          addTab({ id: "tab-credentials-view", kind: "credentialsText", title: "凭据视图", credView: "text", closable: true }),
      },
      ...(
        [
          ["system", "主题：跟随系统"],
          ["light", "主题：浅色"],
          ["dark", "主题：深色"],
        ] as const
      ).map(([value, label]) => ({
        id: `theme-${value}`,
        label,
        hint: themeMode === value ? "当前" : undefined,
        icon: IconMonitor,
        run: () => setThemeMode(value),
      })),
      {
        id: "audit",
        label: "查看审计日志",
        hint: "用户与 AI 的动作记录",
        icon: IconHistory,
        run: () => addTab({ id: "audit", kind: "audit", title: "审计日志", closable: true }),
      },
      {
        id: "kill-terminal",
        label: "结束当前终端进程…",
        hint: "进程会结束，无法恢复",
        icon: IconTerminal,
        run: () => {
          const st = useUi.getState();
          const ws = st.workspaces.find((item) => item.id === st.activeWorkspaceId);
          const pane = ws?.panes.find((item) => item.id === ws.activePaneId) ?? ws?.panes[0];
          const tab =
            pane?.tabs.find((item) => item.id === pane.activeTabId) ??
            pane?.tabs[pane.tabs.length - 1];
          if (!tab || tab.kind !== "terminal" || !tab.tabId || tab.dead || tab.exited) {
            pushToast("info", "当前标签不是运行中的终端");
            return;
          }
          void requestKillTab(tab.id);
        },
      },
    ];
    const isHidden = (id: string) => hiddenIds.includes(id);
    for (const asset of assets.filter((a) => showHidden || !isHidden(a.id))) {
      const hidden = isHidden(asset.id);
      const connecting = connectingAssetIds.includes(asset.id);
      list.push({
        id: `conn-${asset.id}`,
        label: `连接 ${asset.name}`,
        hint: connecting ? "连接中…" : [asset.host, asset.kind].filter(Boolean).join(" · "),
        icon: assetIcon(asset.kind),
        dot: reachEntries[asset.id],
        run: () => void connectAsset(asset),
      });
      if (asset.host) {
        list.push({
          id: `probe-${asset.id}`,
          label: `探测 ${asset.name}`,
          hint: "端口可达性",
          icon: IconPlug,
          run: () => void runProbeOne(asset),
        });
      }
      list.push({
        id: `edit-${asset.id}`,
        label: `编辑资产 ${asset.name}`,
        hint: "主机 / 端口 / 凭据",
        icon: IconEdit,
        run: () => onEditAsset?.(asset),
      });
      list.push({
        id: `clone-${asset.id}`,
        label: `克隆资产 ${asset.name}`,
        hint: "共享凭据引用",
        icon: IconCopy,
        run: () => void runClone(asset),
      });
      list.push({
        id: `${hidden ? "unhide" : "hide"}-${asset.id}`,
        label: `${hidden ? "取消隐藏" : "隐藏"}资产 ${asset.name}`,
        hint: hidden ? "恢复显示" : "仅界面隐藏",
        icon: hidden ? IconEye : IconEyeOff,
        run: () => (hidden ? unhideAsset(asset) : hideAsset(asset)),
      });
    }
    for (const s of snippets) {
      list.push({
        id: `snippet-${s.id}`,
        label: `插入片段 ${s.name}`,
        hint: snippetHint(s.body),
        icon: IconCommand,
        run: () => void insertSnippet(s),
      });
    }
    return list;
  }, [
    assets,
    snippets,
    hiddenIds,
    showHidden,
    sessions,
    setSessions,
    addTab,
    pushToast,
    onOpenFiles,
    onEditAsset,
    onQuickConnect,
    bindings,
    themeMode,
    setThemeMode,
    reachEntries,
    connectingAssetIds,
    runProbeOne,
  ]);

  const filtered = actions.filter((action) =>
    `${action.label} ${action.hint ?? ""}`.toLowerCase().includes(query.toLowerCase()),
  );
  const activeIndex = filtered.length > 0 ? Math.min(cursor, filtered.length - 1) : 0;
  const activeOptionId = filtered[activeIndex] ? `${listId}-option-${activeIndex}` : undefined;

  useEffect(() => {
    if (!activeOptionId) return;
    const option = document.getElementById(activeOptionId);
    if (typeof option?.scrollIntoView === "function") option.scrollIntoView({ block: "nearest" });
  }, [activeOptionId]);

  const closePalette = () => {
    if (closedRef.current) return;
    closedRef.current = true;
    onClose();
  };
  const execute = (action: Action) => {
    if (closedRef.current) return;
    closedRef.current = true;
    try {
      action.run();
    } finally {
      onClose();
    }
  };
  const moveCursor = (next: number) => {
    if (filtered.length > 0) setCursor((next + filtered.length) % filtered.length);
  };
  const onPaletteKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Tab") {
      trapOverlayTab(event, modalRef.current);
      return;
    }
    if (isImeKeyEvent(event)) return;
    if (event.key === "Escape" && !event.repeat) {
      event.preventDefault();
      closePalette();
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      moveCursor(activeIndex + 1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveCursor(activeIndex - 1);
    } else if (event.key === "Home") {
      event.preventDefault();
      moveCursor(0);
    } else if (event.key === "End") {
      event.preventDefault();
      moveCursor(filtered.length - 1);
    } else if (event.key === "Enter" && !event.repeat && filtered[activeIndex]) {
      event.preventDefault();
      execute(filtered[activeIndex]);
    }
  };

  return (
    <div className="nx-overlay items-start justify-center pt-24" onClick={closePalette}>
      <div
        ref={modalRef}
        className="nx-modal nx-command-modal w-[560px] overflow-hidden p-0"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={helpId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onPaletteKeyDown}
      >
        <h2 id={titleId} className="nx-sr-only">命令面板</h2>
        <div className="flex items-center gap-2.5 border-b border-neutral-800/70 px-4">
          <IconSearch size={15} className="shrink-0 text-neutral-400" />
          <input
            ref={inputRef}
            className="nx-command-input border-none px-0"
            placeholder="输入命令或资产名…"
            aria-label="命令或资产搜索"
            role="combobox"
            aria-autocomplete="list"
            aria-expanded="true"
            aria-controls={listId}
            aria-activedescendant={activeOptionId}
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setCursor(0);
            }}
          />
        </div>
        <div id={listId} className="max-h-[340px] overflow-y-auto p-1.5" role="listbox" aria-label="命令结果" tabIndex={-1}>
          {filtered.map((action, index) => (
            <button
              key={action.id}
              id={`${listId}-option-${index}`}
              type="button"
              role="option"
              aria-selected={index === activeIndex}
              tabIndex={-1}
              className={`nx-command-item ${index === activeIndex ? "is-active" : ""}`}
              onMouseEnter={() => setCursor(index)}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => execute(action)}
            >
              <action.icon size={15} className="shrink-0 text-neutral-400" />
              <span className="min-w-0 flex-1 truncate">{action.label}</span>
              <ReachabilityDot entry={action.dot} />
              {action.hint && <span className="nx-command-hint shrink-0 text-[11px]">{action.hint}</span>}
            </button>
          ))}
          {filtered.length === 0 && (
            <div className="nx-command-empty px-4 py-8 text-center text-xs" role="status">
              没有匹配的命令或资产
            </div>
          )}
        </div>
        {assetError && (
          <div className="nx-command-error" role="alert">
            <span className="min-w-0 flex-1 truncate" title={assetError}>{assetError}</span>
            <button
              type="button"
              className="nx-link shrink-0"
              onClick={() => {
                inputRef.current?.focus();
                void assetsQuery.refetch();
              }}
            >
              重试
            </button>
          </div>
        )}
        {snippetError && (
          <div className="nx-command-error" role="alert">
            <span className="min-w-0 flex-1 truncate" title={snippetError}>{snippetError}</span>
            <button
              type="button"
              className="nx-link shrink-0"
              onClick={() => {
                inputRef.current?.focus();
                void snippetsQuery.refetch();
              }}
            >
              重试
            </button>
          </div>
        )}
        <div id={helpId} className="nx-command-help flex items-center gap-3 border-t border-neutral-800/70 bg-neutral-950/40 px-4 py-2 text-[10.5px]">
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">↑</span>
            <span className="nx-kbd">↓</span> 选择
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Enter</span> 执行
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Esc</span> 关闭
          </span>
        </div>
      </div>
    </div>
  );
}
