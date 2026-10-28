import { useCallback, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useUi, nextTabId, openTerminalTab, requestKillTab, reconnectWorkspaceConnection, workspaceConnectionState, workspaceNeedsReconnect, type Workspace, useSessionList, useWorkspaceList } from "../store";
import { formatBinding, useKeybindings, type KeybindingActionId } from "../keybindings";
import { sessionApi, type Asset } from "../../ipc/commands";
import type { SnippetDto } from "../../ipc/types";
import { describeError } from "../../ui/errorText";
import { isMobileShell } from "../shell";
import { useBackgroundSessionsAccess } from "../../features/terminal/backgroundSessionsAccess";
import { insertSnippet } from "../../features/explorer/snippetInsert";
import { cloneAsset } from "../../features/explorer/assetClone";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { createMultiHostLogTabId, MULTI_HOST_LOG_TAB_ID } from "../../features/logs/logTailTab";
import { createFleetHealthTabId, FLEET_HEALTH_TAB_ID } from "../../features/fleet/fleetHealthTab";
import { workspaceLayoutTree } from "../paneLayout";
import { resolveActivePane } from "../splitTree";
import {
  IconActivity,
  IconClock,
  IconCommand,
  IconCopy,
  IconDatabase,
  IconEdit,
  IconEye,
  IconEyeOff,
  IconHistory,
  IconMergeH,
  IconMonitor,
  IconNetwork,
  IconRefresh,
  IconSettings,
  IconSplitH,
  IconSplitV,
  IconTerminal,
  IconZap,
  assetIcon,
} from "../../ui/icons";

export interface JumpCommand {
  id: string;
  label: string;
  hint?: string;
  section?: string;
  icon: typeof IconTerminal;
  // keepOpen leaves the palette open after running, for commands that change what it shows.
  keepOpen?: boolean;
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

export interface JumpCommandOptions {
  assets: Asset[];
  snippets: SnippetDto[];
  onEditAsset?: (asset: Asset) => void;
  onShowHosts: () => void;
}

// useJumpCommands lists the jump palette's commands: app actions, open workspaces, asset management and
// snippets. Connecting to an asset is a host row, not a command.
export function useJumpCommands({ assets, snippets, onEditAsset, onShowHosts }: JumpCommandOptions): JumpCommand[] {
  const qc = useQueryClient();
  const sessions = useSessionList();
  const setSessions = useUi((s) => s.setSessions);
  const addTab = useUi((s) => s.addTab);
  const pushToast = useUi((s) => s.pushToast);
  const themeMode = useUi((s) => s.themeMode);
  const setThemeMode = useUi((s) => s.setThemeMode);
  const workspaces = useWorkspaceList();
  const activeWorkspaceId = useUi((s) => s.activeWorkspaceId);
  const { hiddenIds, showHidden } = useAssetVisibility();
  const bindings = useKeybindings();
  const keyHint = useCallback((id: KeybindingActionId, description?: string): string | undefined => {
    const binding = bindings[id];
    if (!binding) return description;
    return description ? `${formatBinding(binding)} · ${description}` : formatBinding(binding);
  }, [bindings]);

  const runClone = useCallback(async (asset: Asset) => {
    try {
      const created = await cloneAsset(asset);
      if (!created) return;
      await qc.invalidateQueries({ queryKey: ["assets"] });
      pushToast("success", `已复制为「${created.name}」`);
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  }, [qc, pushToast]);

  const hideAsset = useCallback((asset: Asset) => {
    useAssetVisibility.getState().hide(asset.id);
    pushToast("info", `已隐藏「${asset.name}」，可点资产树里的眼睛按钮找回`);
  }, [pushToast]);

  const unhideAsset = useCallback((asset: Asset) => {
    useAssetVisibility.getState().unhide(asset.id);
    pushToast("info", `已取消隐藏「${asset.name}」`);
  }, [pushToast]);

  const backgroundAvailable = useBackgroundSessionsAccess(workspaces.find((workspace) => workspace.id === activeWorkspaceId) ?? workspaces[workspaces.length - 1]);
  return useMemo<JumpCommand[]>(() => {
    const currentWorkspace = workspaces.find((workspace) => workspace.id === activeWorkspaceId) ??
      workspaces[workspaces.length - 1];
    const workspaceActions: JumpCommand[] = workspaces.map((workspace) => ({
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
    const reconnectCommands: JumpCommand[] =
      currentWorkspace && workspaceNeedsReconnect(workspaceConnectionState(currentWorkspace, sessions))
        ? [{
            id: "reconnect-workspace",
            label: "重新连接当前工作区",
            hint: currentWorkspace.title,
            icon: IconRefresh,
            run: () => void reconnectWorkspaceConnection(currentWorkspace.id),
          }]
        : [];
    const list: JumpCommand[] = [
      ...reconnectCommands,
      {
        id: "quick-connect",
        label: "快速连接…",
        hint: keyHint("quickConnect", "只看主机 · Tab 切换"),
        icon: IconZap,
        keepOpen: true,
        run: onShowHosts,
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
            } satisfies JumpCommand,
          ]),
      ...([
        { id: "split-right", label: "向右分屏", direction: "right", icon: IconSplitV },
        { id: "split-down", label: "向下分屏", direction: "down", icon: IconSplitH },
      ] as const).map(({ id, label, direction, icon }): JumpCommand => ({
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
      ] as const).map(({ id, label, edge, icon }): JumpCommand => ({
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
        hint: "当前项目的主机状态 · 只读 · 手动刷新",
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
        hint: "已安装设备代理的设备",
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
        hint: "用户和 AI 的操作记录",
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
      list.push({
        id: `edit-${asset.id}`,
        label: `编辑资产 ${asset.name}`,
        section: "资产操作",
        hint: "主机 / 端口 / 凭据",
        icon: IconEdit,
        run: () => onEditAsset?.(asset),
      });
      list.push({
        id: `clone-${asset.id}`,
        label: `复制资产 ${asset.name}`,
        section: "资产操作",
        hint: "与原资产共用凭据",
        icon: IconCopy,
        run: () => void runClone(asset),
      });
      list.push({
        id: `${hidden ? "unhide" : "hide"}-${asset.id}`,
        label: `${hidden ? "取消隐藏" : "隐藏"}资产 ${asset.name}`,
        section: "资产操作",
        hint: hidden ? "恢复显示" : "仅界面隐藏",
        icon: hidden ? IconEye : IconEyeOff,
        run: () => (hidden ? unhideAsset(asset) : hideAsset(asset)),
      });
    }
    for (const s of snippets) {
      list.push({
        id: `snippet-${s.id}`,
        label: `插入片段 ${s.name}`,
        section: "片段",
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
    onEditAsset,
    onShowHosts,
    themeMode,
    setThemeMode,
    workspaces,
    activeWorkspaceId,
    hideAsset,
    keyHint,
    qc,
    runClone,
    unhideAsset,
  ]);
}
