import { Fragment, useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useUi, connectAsset, nextTabId, openTerminalTab, requestKillTab, type Workspace, useSessionList, useWorkspaceList } from "./store";
import { formatBinding, useKeybindings, type KeybindingActionId } from "./keybindings";
import { assetApi, sessionApi, type Asset } from "../ipc/commands";
import { describeError } from "../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../ui/DialogHost";
import { isMobileShell } from "./shell";
import { useBackgroundSessionsAccess } from "../features/terminal/backgroundSessionsAccess";
import { insertSnippet } from "../features/explorer/snippetInsert";
import { cloneAsset } from "../features/explorer/assetClone";
import { isInventoryOnlyKind } from "../features/explorer/inventory";
import { useAssetVisibility } from "../features/explorer/assetVisibility";
import { useAssetReachability, type ReachabilityEntry } from "../features/explorer/assetReachability";
import { ReachabilityDot } from "../features/explorer/ReachabilityDot";
import { createMultiHostLogTabId, MULTI_HOST_LOG_TAB_ID } from "../features/logs/logTailTab";
import { createFleetHealthTabId, FLEET_HEALTH_TAB_ID } from "../features/fleet/fleetHealthTab";
import { workspaceLayoutTree } from "./paneLayout";
import { resolveActivePane } from "./splitTree";
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
  IconMergeH,
  IconMonitor,
  IconNetwork,
  IconRefresh,
  IconSearch,
  IconSettings,
  IconSplitH,
  IconSplitV,
  IconTerminal,
  IconZap,
  assetIcon,
} from "../ui/icons";

interface Action {
  id: string;
  label: string;
  hint?: string;
  section?: string;
  icon: typeof IconTerminal;
  dot?: ReachabilityEntry;
  run: () => void;
}

function snippetHint(body: string): string {
  const first = body.split(/\r?\n/, 1)[0]?.trim() ?? "";
  return first.length > 36 ? `${first.slice(0, 36)}…` : first;
}

function workspaceIcon(workspace: Workspace) {
  if (workspace.kind === "db") return IconDatabase;
  if (workspace.kind === "tools") return IconSettings;
  return assetIcon(workspace.assetKind ?? "ssh");
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
  const sessions = useSessionList();
  const setSessions = useUi((s) => s.setSessions);
  const addTab = useUi((s) => s.addTab);
  const pushToast = useUi((s) => s.pushToast);
  const themeMode = useUi((s) => s.themeMode);
  const setThemeMode = useUi((s) => s.setThemeMode);
  const connectingAssetIds = useUi((s) => s.connectingAssetIds);
  const workspaces = useWorkspaceList();
  const activeWorkspaceId = useUi((s) => s.activeWorkspaceId);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const snippetsQuery = useQuery({ queryKey: ["snippets"], queryFn: () => assetApi.snippetList(), retry: false });
  const assets = useMemo(() => assetsQuery.data ?? [], [assetsQuery.data]);
  const snippets = useMemo(() => snippetsQuery.data ?? [], [snippetsQuery.data]);
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
  const keyHint = useCallback((id: KeybindingActionId, description?: string): string | undefined => {
    const binding = bindings[id];
    if (!binding) return description;
    return description ? `${formatBinding(binding)} · ${description}` : formatBinding(binding);
  }, [bindings]);

  useEffect(() => {
    if (probedRef.current || assets.length === 0) return;
    probedRef.current = true;
    const ids = assets
      .filter((a) => !isInventoryOnlyKind(a.kind) && Boolean(a.host) && (showHidden || !hiddenIds.includes(a.id)))
      .slice(0, 16)
      .map((a) => a.id);
    if (ids.length > 0) void useAssetReachability.getState().probe(ids);
  }, [assets, hiddenIds, showHidden]);

  const runClone = useCallback(async (asset: Asset) => {
    try {
      const created = await cloneAsset(asset);
      if (!created) return;
      await qc.invalidateQueries({ queryKey: ["assets"] });
      pushToast("success", `已克隆为「${created.name}」`);
    } catch (e) {
      pushToast("error", `克隆失败：${describeError(e)}`);
    }
  }, [qc, pushToast]);

  const hideAsset = useCallback((asset: Asset) => {
    useAssetVisibility.getState().hide(asset.id);
    pushToast("info", `已隐藏「${asset.name}」— 资产树眼睛按钮可找回`);
  }, [pushToast]);

  const unhideAsset = useCallback((asset: Asset) => {
    useAssetVisibility.getState().unhide(asset.id);
    pushToast("info", `已取消隐藏「${asset.name}」`);
  }, [pushToast]);

  const backgroundAvailable = useBackgroundSessionsAccess(workspaces.find((workspace) => workspace.id === activeWorkspaceId) ?? workspaces[workspaces.length - 1]);
  const actions = useMemo<Action[]>(() => {
    const currentWorkspace = workspaces.find((workspace) => workspace.id === activeWorkspaceId) ??
      workspaces[workspaces.length - 1];
    const workspaceActions: Action[] = workspaces.map((workspace) => ({
      id: `workspace-${workspace.id}`,
      label: workspace.title,
      section: "已打开的工作区",
      hint: [
        workspace.id === currentWorkspace?.id ? "当前" : null,
        `${workspace.panes.flatMap((pane) => pane.tabs).length} 个标签`,
        workspace.panes.length > 1 ? "已分屏" : null,
      ].filter(Boolean).join(" · "),
      icon: workspaceIcon(workspace),
      run: () => {
        const current = useUi.getState();
        if (current.workspaces.some((item) => item.id === workspace.id)) {
          current.setActiveWorkspace(workspace.id);
        }
      },
    }));
    const list: Action[] = [
      {
        id: "quick-connect",
        label: "快速连接…",
        hint: keyHint("quickConnect", "最近使用优先"),
        icon: IconZap,
        run: () => onQuickConnect?.(),
      },
      ...(isMobileShell()
        ? []
        : [
            {
              id: "local-terminal",
              label: "打开本地终端",
              hint: "当前设备",
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
            } satisfies Action,
          ]),
      ...([
        { id: "split-right", label: "向右分屏", direction: "right", icon: IconSplitV },
        { id: "split-down", label: "向下分屏", direction: "down", icon: IconSplitH },
      ] as const).map(({ id, label, direction, icon }): Action => ({
        id,
        label,
        hint: keyHint(direction === "right" ? "splitRight" : "splitDown", "拆分当前窗格"),
        icon,
        run: () => {
          const current = useUi.getState();
          const workspace = current.workspaces.find((item) => item.id === current.activeWorkspaceId);
          if (!workspace) {
            pushToast("info", "先连接一台主机（双击左侧资产）");
            return;
          }
          current.splitPane(direction, undefined, workspace.id);
        },
      })),
      {
        id: "close-pane",
        label: "关闭当前窗格（保留标签页）",
        hint: keyHint("closePane", "标签页移到相邻窗格"),
        icon: IconMergeH,
        run: () => {
          const current = useUi.getState();
          const workspace = current.workspaces.find((item) => item.id === current.activeWorkspaceId);
          if (!workspace || workspace.panes.length < 2) {
            pushToast("info", "当前工作区没有分屏");
            return;
          }
          current.closePane(workspace.activePaneId, workspace.id);
        },
      },
      // Keyboard equivalents of dropping the current tab on the current pane's right or bottom edge.
      ...([
        { id: "tab-to-new-pane-right", label: "移到右侧新窗格", edge: "right", icon: IconSplitV },
        { id: "tab-to-new-pane-down", label: "移到下方新窗格", edge: "bottom", icon: IconSplitH },
      ] as const).map(({ id, label, edge, icon }): Action => ({
        id,
        label,
        hint: "把当前标签移到新拆分的窗格",
        icon,
        run: () => {
          const current = useUi.getState();
          const workspace = current.workspaces.find((item) => item.id === current.activeWorkspaceId);
          const tree = workspace ? workspaceLayoutTree(workspace) : null;
          // The pane the user sees as focused, even when the stored id is stale.
          const paneId = tree ? resolveActivePane(tree, workspace?.activePaneId) : null;
          const pane = workspace?.panes.find((item) => item.id === paneId);
          const tabId = pane?.activeTabId ?? pane?.tabs[pane.tabs.length - 1]?.id;
          if (!workspace || !pane || !tabId) {
            pushToast("info", "当前窗格没有标签");
            return;
          }
          current.splitPaneWithTab(tabId, pane.id, edge, workspace.id);
        },
      })),
      {
        id: "wide-files",
        label: "打开文件浏览器",
        hint: "显示文件大小和修改时间列",
        icon: IconFolderOpen,
        run: () => onOpenFiles?.(),
      },
      {
        id: "port-forward",
        label: "打开端口转发",
        hint: "本地 / SOCKS5",
        icon: IconNetwork,
        run: () => {
          const current = useUi.getState();
          const workspace =
            current.workspaces.find((item) => item.id === current.activeWorkspaceId) ??
            current.workspaces[current.workspaces.length - 1];
          const sessionId = workspace?.kind === "session" ? workspace.sessionId : undefined;
          if (!sessionId) {
            current.pushToast("info", "先连接一台主机（双击左侧资产）");
            return;
          }
          current.addTab({
            id: nextTabId(`forward-${sessionId}`),
            kind: "forward",
            title: "端口转发",
            sessionId,
            closable: true,
          });
        },
      },
      {
        id: "forward-overview",
        label: "查看全部端口转发",
        hint: "所有会话",
        icon: IconNetwork,
        run: () =>
          addTab({
            id: "forward-overview",
            kind: "forward",
            title: "端口转发",
            closable: true,
          }),
      },
      {
        id: "exec-batch",
        label: "批量执行命令",
        hint: "已连接 SSH / 设备",
        icon: IconZap,
        run: () => addTab({ id: "exec-batch", kind: "execBatch", title: "批量执行", closable: true }),
      },
      {
        id: MULTI_HOST_LOG_TAB_ID,
        label: "多主机日志合并",
        hint: "文件 / journalctl / Docker · 已连接 SSH / 设备",
        icon: IconCommand,
        run: () => addTab({ id: createMultiHostLogTabId(), kind: "log", title: "多主机日志", closable: true }),
      },
      {
        id: FLEET_HEALTH_TAB_ID,
        label: "主机概览",
        hint: "当前项目 · 只读健康网格 · 默认手动刷新",
        icon: IconActivity,
        run: () => addTab({ id: createFleetHealthTabId(), kind: "log", title: "主机概览", closable: true }),
      },
      {
        id: "settings",
        label: "打开设置",
        hint: "AI / 凭据库",
        icon: IconSettings,
        run: () => addTab({ id: "settings", kind: "settings", title: "设置", closable: true }),
      },
      ...(backgroundAvailable ? [{
        id: "background",
        label: "打开后台会话",
        hint: "查看主机上转入后台的终端进程",
        icon: IconActivity,
        run: () => {
          const current = useUi.getState();
          const workspace =
            current.workspaces.find((item) => item.id === current.activeWorkspaceId) ??
            current.workspaces[current.workspaces.length - 1];
          if (workspace?.kind !== "session" || !workspace.sessionId) {
            current.pushToast("info", "先连接一台主机（双击左侧资产）");
            return;
          }
          current.addTab({
            id: `background-${workspace.id}`,
            kind: "background",
            title: "后台会话",
            sessionId: workspace.sessionId,
            closable: true,
          });
        },
      }] : []),
      {
        id: "history",
        label: "打开终端历史",
        hint: "回看和搜索终端输出",
        icon: IconClock,
        run: () => addTab({ id: "history", kind: "history", title: "终端历史", closable: true }),
      },
      {
        id: "refresh-status",
        label: "刷新会话列表与凭据库状态",
        icon: IconRefresh,
        run: () => {
          void useUi.getState().resyncSessions();
          void qc.invalidateQueries({ queryKey: ["vault-status"] });
        },
      },
      {
        id: "devices",
        label: "打开设备管理",
        hint: "已安装 agent 的设备",
        icon: IconMonitor,
        run: () => addTab({ id: "devices", kind: "devices", title: "设备管理", closable: true }),
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
    list.push(...workspaceActions);
    const isHidden = (id: string) => hiddenIds.includes(id);
    for (const asset of assets.filter((a) => showHidden || !isHidden(a.id))) {
      const hidden = isHidden(asset.id);
      const connecting = connectingAssetIds.includes(asset.id);
      if (!isInventoryOnlyKind(asset.kind)) {
        list.push({
          id: `conn-${asset.id}`,
          label: `连接 ${asset.name}`,
          hint: connecting ? "连接中…" : [asset.host, asset.kind].filter(Boolean).join(" · "),
          icon: assetIcon(asset.kind, typeof asset.options?.icon === "string" ? asset.options.icon : undefined),
          dot: reachEntries[asset.id],
          run: () => void connectAsset(asset),
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
    backgroundAvailable,
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
    themeMode,
    setThemeMode,
    reachEntries,
    connectingAssetIds,
    workspaces,
    activeWorkspaceId,
    hideAsset,
    keyHint,
    qc,
    runClone,
    unhideAsset,
  ]);

  const filtered = actions.filter((action) =>
    `${action.section ?? ""} ${action.label} ${action.hint ?? ""}`.toLowerCase().includes(query.toLowerCase()),
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
            className="nx-command-input"
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
            <Fragment key={action.id}>
              {action.section && filtered[index - 1]?.section !== action.section && (
                <div
                  className="px-3 pb-1 pt-2 text-[10.5px] font-medium text-neutral-500"
                  data-command-section={action.section}
                >
                  {action.section}
                </div>
              )}
              <button
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
            </Fragment>
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
