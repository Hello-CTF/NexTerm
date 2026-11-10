import { useCallback, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useUi, nextTabId, hostHistoryTab, openTerminalTab, requestKillTab, reconnectWorkspaceConnection, workspaceConnectionState, workspaceNeedsReconnect, type Workspace, useSessionList, useWorkspaceList } from "../store";
import { formatBinding, useKeybindings, type KeybindingActionId } from "../keybindings";
import { sessionApi, type Asset } from "../../ipc/commands";
import type { SnippetDto } from "../../ipc/types";
import { describeError } from "../../ui/errorText";
import { isMobileShell } from "../shell";
import { useBackgroundSessionsAccess } from "../../features/terminal/backgroundSessionsAccess";
import { executeSnippet, insertSnippet, isMultiStep } from "../../features/explorer/snippetInsert";
import { analyzeSnippetSteps } from "../../features/explorer/snippetVariables";
import { cloneAsset } from "../../features/explorer/assetClone";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { createMultiHostLogTabId, MULTI_HOST_LOG_TAB_ID } from "../../features/logs/logTailTab";
import { createFleetHealthTabId, FLEET_HEALTH_TAB_ID } from "../../features/fleet/fleetHealthTab";
import { terminalLinkCommands } from "./terminalLinkCommands";
import { terminalLinkSource } from "../../features/terminal/terminalLinkRegistry";
import { workspaceLayoutTree } from "../paneLayout";
import { resolveActivePane } from "../splitTree";
import { requestAppLock } from "../appLock";
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
  IconLock,
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
import { t } from "../../i18n";

export interface JumpCommand {
  id: string;
  label: string;
  hint?: string;
  section?: string;
  icon: typeof IconTerminal;
  // keepOpen leaves the palette open after running, for commands that change what it shows.
  keepOpen?: boolean;
  // ephemeral commands (an id that names a position, such as the Nth link on screen) are not remembered for ranking.
  ephemeral?: boolean;
  run: () => void;
}

function snippetHint(snippet: SnippetDto): string {
  const steps = snippet.steps ?? [];
  const source = steps.length > 0 ? steps[0].command : snippet.body;
  const first = source.split(/\r?\n/, 1)[0]?.trim() ?? "";
  const text = first.length > 36 ? `${first.slice(0, 36)}…` : first;
  return steps.length > 0 ? t("{length} 步 · {text}", { length: steps.length, text }) : text;
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
      pushToast("success", t("已复制为「{name}」", { name: created.name }));
    } catch (e) {
      pushToast("error", t("复制失败：{value}", { value: describeError(e) }));
    }
  }, [qc, pushToast]);

  const hideAsset = useCallback((asset: Asset) => {
    useAssetVisibility.getState().hide(asset.id);
    pushToast("info", t("已隐藏「{name}」，可通过资产树中的眼睛图标恢复显示", { name: asset.name }));
  }, [pushToast]);

  const unhideAsset = useCallback((asset: Asset) => {
    useAssetVisibility.getState().unhide(asset.id);
    pushToast("info", t("已取消隐藏「{name}」", { name: asset.name }));
  }, [pushToast]);

  const backgroundAvailable = useBackgroundSessionsAccess(workspaces.find((workspace) => workspace.id === activeWorkspaceId) ?? workspaces[workspaces.length - 1]);
  const linkSource = useMemo(() => {
    const ws = workspaces.find((workspace) => workspace.id === activeWorkspaceId) ?? workspaces[workspaces.length - 1];
    const pane = ws?.panes.find((item) => item.id === ws.activePaneId) ?? ws?.panes[0];
    const tab = pane?.tabs.find((item) => item.id === pane.activeTabId) ?? pane?.tabs[pane.tabs.length - 1];
    return tab?.kind === "terminal" ? terminalLinkSource(tab.id) : undefined;
  }, [workspaces, activeWorkspaceId]);
  const linkHint = useMemo(() => keyHint("terminalLinks"), [keyHint]);
  const linkCommands = useMemo(
    () => (linkSource ? terminalLinkCommands(linkSource, linkHint) : []),
    [linkSource, linkHint],
  );
  const base = useMemo(() => {
    const currentWorkspace = workspaces.find((workspace) => workspace.id === activeWorkspaceId) ??
      workspaces[workspaces.length - 1];
    const workspaceActions: JumpCommand[] = workspaces.map((workspace) => ({
      id: `workspace-${workspace.id}`,
      label: workspace.title,
      section: t("已打开的工作区"),
      hint: [
        workspace.id === currentWorkspace?.id ? t("当前") : null,
        t("{length} 个标签页", { length: workspace.panes.flatMap((pane) => pane.tabs).length }),
        workspace.panes.length > 1 ? t("已分屏") : null,
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
            label: t("重新连接当前工作区"),
            hint: currentWorkspace.title,
            icon: IconRefresh,
            run: () => void reconnectWorkspaceConnection(currentWorkspace.id),
          }]
        : [];
    const list: JumpCommand[] = [
      ...reconnectCommands,
      {
        id: "quick-connect",
        label: t("快速连接…"),
        hint: keyHint("quickConnect", t("只看主机 · Tab 切换")),
        icon: IconZap,
        keepOpen: true,
        run: onShowHosts,
      },
      ...(isMobileShell()
        ? []
        : [
            {
              id: "local-terminal",
              label: t("打开本地终端"),
              hint: t("当前设备"),
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
        { id: "split-right", label: t("向右分屏"), direction: "right", icon: IconSplitV },
        { id: "split-down", label: t("向下分屏"), direction: "down", icon: IconSplitH },
      ] as const).map(({ id, label, direction, icon }): JumpCommand => ({
        id,
        label,
        hint: keyHint(direction === "right" ? "splitRight" : "splitDown", t("拆分当前窗格")),
        icon,
        run: () => {
          const current = useUi.getState();
          const workspace = current.workspaces.find((item) => item.id === current.activeWorkspaceId);
          if (!workspace) {
            pushToast("info", t("请先连接主机（双击左侧资产）"));
            return;
          }
          current.splitPane(direction, undefined, workspace.id);
        },
      })),
      {
        id: "close-pane",
        label: t("关闭当前窗格（保留标签页）"),
        hint: keyHint("closePane", t("标签页将移到相邻窗格")),
        icon: IconMergeH,
        run: () => {
          const current = useUi.getState();
          const workspace = current.workspaces.find((item) => item.id === current.activeWorkspaceId);
          if (!workspace || workspace.panes.length < 2) {
            pushToast("info", t("当前工作区未分屏"));
            return;
          }
          current.closePane(workspace.activePaneId, workspace.id);
        },
      },
      // Keyboard equivalents of dropping the current tab on the current pane's right or bottom edge.
      ...([
        { id: "tab-to-new-pane-right", label: t("移到右侧新窗格"), edge: "right", icon: IconSplitV },
        { id: "tab-to-new-pane-down", label: t("移到下方新窗格"), edge: "bottom", icon: IconSplitH },
      ] as const).map(({ id, label, edge, icon }): JumpCommand => ({
        id,
        label,
        hint: t("将当前标签页移到新拆分的窗格"),
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
            pushToast("info", t("当前窗格无标签页"));
            return;
          }
          current.splitPaneWithTab(tabId, pane.id, edge, workspace.id);
        },
      })),
      {
        id: "port-forward",
        label: t("打开端口转发"),
        hint: t("本地 / SOCKS5"),
        icon: IconNetwork,
        run: () => {
          const current = useUi.getState();
          const workspace =
            current.workspaces.find((item) => item.id === current.activeWorkspaceId) ??
            current.workspaces[current.workspaces.length - 1];
          const sessionId = workspace?.kind === "session" ? workspace.sessionId : undefined;
          if (!sessionId) {
            current.pushToast("info", t("请先连接主机（双击左侧资产）"));
            return;
          }
          current.addTab({
            id: nextTabId(`forward-${sessionId}`),
            kind: "forward",
            title: t("端口转发"),
            sessionId,
            closable: true,
          });
        },
      },
      {
        id: "forward-overview",
        label: t("查看全部端口转发"),
        hint: t("所有会话"),
        icon: IconNetwork,
        run: () =>
          addTab({
            id: "forward-overview",
            kind: "forward",
            title: t("端口转发"),
            closable: true,
          }),
      },
      {
        id: "exec-batch",
        label: t("批量执行命令"),
        hint: t("已连接 SSH / 设备"),
        icon: IconZap,
        run: () => addTab({ id: "exec-batch", kind: "execBatch", title: t("批量执行"), closable: true }),
      },
      {
        id: MULTI_HOST_LOG_TAB_ID,
        label: t("多主机日志合并"),
        hint: t("文件 / journalctl / Docker · 已连接 SSH / 设备"),
        icon: IconCommand,
        run: () => addTab({ id: createMultiHostLogTabId(), kind: "log", title: t("多主机日志"), closable: true }),
      },
      {
        id: FLEET_HEALTH_TAB_ID,
        label: t("主机概览"),
        hint: t("当前项目的主机状态 · 只读 · 手动刷新"),
        icon: IconActivity,
        run: () => addTab({ id: createFleetHealthTabId(), kind: "log", title: t("主机概览"), closable: true }),
      },
      {
        id: "settings",
        label: t("打开设置"),
        hint: t("AI / 凭据库"),
        icon: IconSettings,
        run: () => addTab({ id: "settings", kind: "settings", title: t("设置"), closable: true }),
      },
      {
        id: "lock-app",
        label: t("锁定 NexTerm"),
        hint: keyHint("lockApp", t("遮挡界面，输入密码后解锁；终端继续运行")),
        icon: IconLock,
        run: () => void requestAppLock(),
      },
      ...(backgroundAvailable ? [{
        id: "background",
        label: t("打开后台会话"),
        hint: t("查看主机上转入后台的终端进程"),
        icon: IconActivity,
        run: () => {
          const current = useUi.getState();
          const workspace =
            current.workspaces.find((item) => item.id === current.activeWorkspaceId) ??
            current.workspaces[current.workspaces.length - 1];
          if (workspace?.kind !== "session" || !workspace.sessionId) {
            current.pushToast("info", t("请先连接主机（双击左侧资产）"));
            return;
          }
          current.addTab({
            id: `background-${workspace.id}`,
            kind: "background",
            title: t("后台会话"),
            sessionId: workspace.sessionId,
            closable: true,
          });
        },
      }] : []),
      {
        id: "history",
        label: t("打开终端历史"),
        hint: t("回看和搜索终端输出"),
        icon: IconClock,
        run: () => {
          const current = useUi.getState();
          const active =
            current.workspaces.find((item) => item.id === current.activeWorkspaceId) ??
            current.workspaces[current.workspaces.length - 1];
          addTab(hostHistoryTab(active) ?? { id: "history", kind: "history", title: t("终端历史"), closable: true });
        },
      },
      {
        id: "refresh-status",
        label: t("刷新会话列表与凭据库状态"),
        icon: IconRefresh,
        run: () => {
          void useUi.getState().resyncSessions();
          void qc.invalidateQueries({ queryKey: ["vault-status"] });
        },
      },
      {
        id: "devices",
        label: t("打开设备管理"),
        hint: t("已安装设备代理的设备"),
        icon: IconMonitor,
        run: () => addTab({ id: "devices", kind: "devices", title: t("设备管理"), closable: true }),
      },
      ...(
        [
          ["system", t("主题：跟随系统")],
          ["light", t("主题：浅色")],
          ["dark", t("主题：深色")],
        ] as const
      ).map(([value, label]) => ({
        id: `theme-${value}`,
        label,
        hint: themeMode === value ? t("当前") : undefined,
        icon: IconMonitor,
        run: () => setThemeMode(value),
      })),
      {
        id: "audit",
        label: t("查看审计日志"),
        hint: t("用户和 AI 的操作记录"),
        icon: IconHistory,
        run: () => addTab({ id: "audit", kind: "audit", title: t("审计日志"), closable: true }),
      },
      {
        id: "kill-terminal",
        label: t("结束当前终端进程…"),
        hint: t("进程会结束，无法恢复"),
        icon: IconTerminal,
        run: () => {
          const st = useUi.getState();
          const ws = st.workspaces.find((item) => item.id === st.activeWorkspaceId);
          const pane = ws?.panes.find((item) => item.id === ws.activePaneId) ?? ws?.panes[0];
          const tab =
            pane?.tabs.find((item) => item.id === pane.activeTabId) ??
            pane?.tabs[pane.tabs.length - 1];
          if (!tab || tab.kind !== "terminal" || !tab.tabId || tab.dead || tab.exited) {
            pushToast("info", t("当前标签页不是运行中的终端"));
            return;
          }
          void requestKillTab(tab.id);
        },
      },
    ];
    const linkInsertAt = list.length;
    list.push(...workspaceActions);
    const isHidden = (id: string) => hiddenIds.includes(id);
    for (const asset of assets.filter((a) => showHidden || !isHidden(a.id))) {
      const hidden = isHidden(asset.id);
      list.push({
        id: `edit-${asset.id}`,
        label: t("编辑资产 {name}", { name: asset.name }),
        section: t("资产操作"),
        hint: t("主机 / 端口 / 凭据"),
        icon: IconEdit,
        run: () => onEditAsset?.(asset),
      });
      list.push({
        id: `clone-${asset.id}`,
        label: t("复制资产 {name}", { name: asset.name }),
        section: t("资产操作"),
        hint: t("与原资产共用凭据"),
        icon: IconCopy,
        run: () => void runClone(asset),
      });
      list.push({
        id: `${hidden ? "unhide" : "hide"}-${asset.id}`,
        label: t("{value}资产 {name}", { value: hidden ? t("取消隐藏") : t("隐藏"), name: asset.name }),
        section: t("资产操作"),
        hint: hidden ? t("恢复显示") : t("仅界面隐藏"),
        icon: hidden ? IconEye : IconEyeOff,
        run: () => (hidden ? unhideAsset(asset) : hideAsset(asset)),
      });
    }
    for (const s of snippets) {
      // A multi-step snippet cannot be inserted; running it confirms each step that asks for it.
      const multi = isMultiStep(s);
      // Variables are only asked for and rendered in the snippets panel.
      const needsPanel = multi && analyzeSnippetSteps((s.steps ?? []).map((x) => x.command)).hasVariables;
      list.push({
        id: `snippet-${s.id}`,
        label: t("{value}片段 {name}", { value: multi ? t("执行") : t("插入"), name: s.name }),
        section: t("片段"),
        hint: snippetHint(s),
        icon: IconCommand,
        run: () => {
          if (needsPanel) pushToast("info", t("带参数的片段请在片段面板中执行"));
          else void (multi ? executeSnippet(s) : insertSnippet(s));
        },
      });
    }
    return { list, linkInsertAt };
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
  return useMemo<JumpCommand[]>(
    () => [...base.list.slice(0, base.linkInsertAt), ...linkCommands, ...base.list.slice(base.linkInsertAt)],
    [base, linkCommands],
  );
}
