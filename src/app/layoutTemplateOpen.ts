import { assetApi, type Asset, type SessionInfo } from "../ipc/commands";
import type { SnippetDto } from "../ipc/types";
import { ask, promptText } from "../ui/dialogs";
import { describeError } from "../ui/errorText";
import { runSnippetAction } from "../features/explorer/snippetInsert";
import { analyzeSnippetSteps } from "../features/explorer/snippetVariables";
import { assetEnvironment } from "../features/production/assetEnvironment";
import { showSettingsSection } from "../features/settings/settingsNavigation";
import { connectAsset, makePane, openTerminalTab, useUi, type Workspace } from "./store";
import { withLayoutTree } from "./paneLayout";
import {
  addLayoutTemplate,
  captureLayoutTemplate,
  layoutTemplatePanes,
  LAYOUT_TEMPLATE_ASSET_KINDS,
  LAYOUT_TEMPLATE_LIMITS,
  getLayoutTemplates,
  workspaceTreeFromTemplate,
  type LayoutTemplate,
} from "./layoutTemplates";
import { t } from "../i18n";

/** How long a pane's terminal may take to attach before its startup snippet is skipped. */
export const LAYOUT_TERMINAL_READY_TIMEOUT_MS = 60_000;

// Asset ids whose template is being opened, so a second open of the same host cannot interleave with the first.
const openingAssets = new Set<string>();

export type OpenLayoutTemplateResult =
  | { ok: true; workspaceId: string; notices: string[] }
  | { ok: false; reason: "missing-asset" | "unsupported" | "occupied" | "canceled" | "failed"; message?: string };

function hostWorkspace(assetId: string): Workspace | undefined {
  return useUi.getState().workspaces.find((workspace) => workspace.kind === "session" && workspace.assetId === assetId);
}

function workspaceHasTabs(workspace: Workspace): boolean {
  return workspace.panes.some((pane) => pane.tabs.length > 0);
}

export function openLayoutTemplateSettings(): void {
  useUi.getState().addTab({ id: "settings", kind: "settings", title: t("设置"), closable: true });
  showSettingsSection("settings-layout-templates");
}

function notifyNotices(template: LayoutTemplate, notices: readonly string[]): void {
  if (notices.length === 0) return;
  useUi.getState().pushToast(
    "info",
    t("布局模板「{name}」：{notices}", { name: template.name, notices: notices.join("；") }),
    { label: t("管理模板"), onSelect: openLayoutTemplateSettings },
  );
}

// terminalOfTab returns the tab's terminal once it attached (its backend terminal id), null when it can no longer
// attach (tab gone from the workspace, or the terminal ended), or undefined while it is still opening. The exact tab
// is looked up anywhere in the workspace, so moving it to another pane does not matter and a different terminal is
// never picked in its place.
function terminalOfTab(workspaceId: string, storeTabId: string): string | null | undefined {
  const workspace = useUi.getState().workspaces.find((candidate) => candidate.id === workspaceId);
  const tab = workspace?.panes.flatMap((pane) => pane.tabs).find((candidate) => candidate.id === storeTabId);
  if (!tab || tab.kind !== "terminal" || tab.dead || tab.exited) return null;
  return tab.tabId ?? undefined;
}

function waitForTabTerminal(workspaceId: string, storeTabId: string, timeoutMs: number): Promise<string | null> {
  return new Promise((resolve) => {
    let settled = false;
    const finish = (value: string | null) => {
      if (settled) return;
      settled = true;
      unsubscribe();
      clearTimeout(timer);
      resolve(value);
    };
    const check = () => {
      const state = terminalOfTab(workspaceId, storeTabId);
      if (state !== undefined) finish(state);
    };
    const unsubscribe = useUi.subscribe(check);
    const timer = setTimeout(() => finish(null), timeoutMs);
    check();
  });
}

interface PaneSnippetRun {
  pane: number;
  paneId: string;
  snippet: SnippetDto;
}

async function plannedSnippetRuns(
  template: LayoutTemplate,
  paneIds: readonly string[],
  notices: string[],
): Promise<PaneSnippetRun[]> {
  const panes = layoutTemplatePanes(template.layout);
  if (!panes.some((pane) => pane.snippetId)) return [];
  let snippets: SnippetDto[];
  try {
    snippets = await assetApi.snippetList();
  } catch (cause) {
    notices.push(t("无法读取片段，未运行启动片段：{value}", { value: describeError(cause) }));
    return [];
  }
  const runs: PaneSnippetRun[] = [];
  panes.forEach((pane, index) => {
    if (!pane.snippetId) return;
    const snippet = snippets.find((candidate) => candidate.id === pane.snippetId);
    if (!snippet) {
      notices.push(t("窗格 {pane} 的启动片段已不存在，已打开空终端", { pane: index + 1 }));
      return;
    }
    const commands = (snippet.steps?.length ?? 0) > 0 ? snippet.steps.map((step) => step.command) : [snippet.body];
    if (analyzeSnippetSteps(commands).hasVariables) {
      notices.push(t("窗格 {pane} 的片段「{name}」带参数，请在片段面板中执行", { pane: index + 1, name: snippet.name }));
      return;
    }
    runs.push({ pane: index + 1, paneId: paneIds[index], snippet });
  });
  return runs;
}

function confirmSnippetRuns(template: LayoutTemplate, asset: Asset, runs: readonly PaneSnippetRun[]): Promise<boolean> {
  const lines = runs.map((run) => t("窗格 {pane}：{name}", { pane: run.pane, name: run.snippet.name })).join("\n");
  const production = assetEnvironment(asset.tags ?? "") === "production";
  const message = production
    ? t("「{host}」是生产环境主机。布局模板「{name}」将在终端就绪后运行以下启动片段：\n\n{lines}\n\n确认运行吗？", { host: asset.name, name: template.name, lines })
    : t("布局模板「{name}」将在终端就绪后运行以下启动片段：\n\n{lines}\n\n确认运行吗？", { name: template.name, lines });
  return ask(message, { title: t("运行启动片段"), kind: production ? "warning" : "info" });
}

/**
 * Opens a template: connects its host through the usual connect flow (vault unlock, credential prompts, host key
 * confirmation), lays the host's workspace out as the template's split tree, opens a terminal per pane (durable or
 * temporary as the default terminal mode decides) and, after the user confirms, runs each pane's snippet once that
 * pane's terminal is ready. Snippets never run without that confirmation.
 */
export async function openLayoutTemplate(
  template: LayoutTemplate,
  options: { readyTimeoutMs?: number } = {},
): Promise<OpenLayoutTemplateResult> {
  if (openingAssets.has(template.assetId)) {
    const message = t("该主机的布局模板正在打开，请稍候");
    useUi.getState().pushToast("info", message);
    return { ok: false, reason: "occupied", message };
  }
  openingAssets.add(template.assetId);
  try {
    return await openTemplate(template, options);
  } finally {
    openingAssets.delete(template.assetId);
  }
}

async function openTemplate(
  template: LayoutTemplate,
  options: { readyTimeoutMs?: number },
): Promise<OpenLayoutTemplateResult> {
  const { pushToast } = useUi.getState();
  const epoch = useUi.getState().accountEpoch;
  const stale = () => useUi.getState().accountEpoch !== epoch;

  let asset: Asset;
  try {
    asset = await assetApi.get(template.assetId);
  } catch (cause) {
    if (stale()) return { ok: false, reason: "canceled" };
    if ((cause as { code?: string } | null)?.code === "not_found") {
      const message = t("布局模板「{name}」引用的主机已不存在，无法打开", { name: template.name });
      pushToast("error", message, { label: t("管理模板"), onSelect: openLayoutTemplateSettings });
      return { ok: false, reason: "missing-asset", message };
    }
    const message = t("无法读取布局模板的主机：{value}", { value: describeError(cause) });
    pushToast("error", message);
    return { ok: false, reason: "failed", message };
  }
  if (stale()) return { ok: false, reason: "canceled" };
  if (!LAYOUT_TEMPLATE_ASSET_KINDS.has(asset.kind)) {
    const message = t("「{name}」不是终端主机，无法按布局模板打开", { name: asset.name });
    pushToast("error", message);
    return { ok: false, reason: "unsupported", message };
  }

  const occupied = (workspace: Workspace | undefined) => {
    if (!workspace || !workspaceHasTabs(workspace)) return null;
    useUi.getState().setActiveWorkspace(workspace.id);
    const message = t("「{name}」的工作区已有标签页，请先关闭后再按布局模板打开", { name: asset.name });
    pushToast("info", message);
    return { ok: false as const, reason: "occupied" as const, message };
  };
  const before = occupied(hostWorkspace(asset.id));
  if (before) return before;

  const outcome = await connectAsset(asset);
  if (stale()) return { ok: false, reason: "canceled" };
  if (!outcome.ok) return { ok: false, reason: outcome.canceled ? "canceled" : "failed", message: outcome.error };
  const workspace = hostWorkspace(asset.id);
  const session: SessionInfo | undefined = useUi.getState().sessions.find((candidate) => candidate.id === workspace?.sessionId);
  if (!workspace || !session) return { ok: false, reason: "failed" };
  const after = occupied(workspace);
  if (after) return after;

  const panes = layoutTemplatePanes(template.layout).map(() => makePane());
  const paneIds = panes.map((pane) => pane.id);
  const tree = workspaceTreeFromTemplate(template.layout, paneIds);
  useUi.setState((state) => ({
    activeWorkspaceId: workspace.id,
    workspaces: state.workspaces.map((candidate) =>
      candidate.id === workspace.id ? withLayoutTree({ ...candidate, panes, activePaneId: paneIds[0] }, tree) : candidate,
    ),
  }));

  const notices: string[] = [];
  const tabIds = new Map<string, string>();
  for (const [index, paneId] of paneIds.entries()) {
    if (stale()) return { ok: false, reason: "canceled" };
    if (!hostWorkspace(asset.id)?.panes.some((pane) => pane.id === paneId)) continue;
    try {
      tabIds.set(paneId, await openTerminalTab(session, undefined, paneId));
    } catch (cause) {
      notices.push(t("窗格 {pane} 未能打开终端：{value}", { pane: index + 1, value: describeError(cause) }));
    }
  }
  if (stale()) return { ok: false, reason: "canceled" };

  const runs = await plannedSnippetRuns(template, paneIds, notices);
  if (runs.length > 0 && !stale()) {
    if (await confirmSnippetRuns(template, asset, runs)) {
      for (const run of runs) {
        if (stale()) return { ok: false, reason: "canceled" };
        const storeTabId = tabIds.get(run.paneId);
        const tabId = storeTabId
          ? await waitForTabTerminal(workspace.id, storeTabId, options.readyTimeoutMs ?? LAYOUT_TERMINAL_READY_TIMEOUT_MS)
          : null;
        if (stale()) return { ok: false, reason: "canceled" };
        if (!tabId) {
          notices.push(t("窗格 {pane} 的终端未就绪，未运行「{name}」", { pane: run.pane, name: run.snippet.name }));
          continue;
        }
        await runSnippetAction(run.snippet, "execute", { tabId });
      }
    } else {
      notices.push(t("未运行启动片段"));
    }
  }
  if (stale()) return { ok: false, reason: "canceled" };
  notifyNotices(template, notices);
  return { ok: true, workspaceId: workspace.id, notices };
}

/** Asks for a name and saves the workspace's layout as a template; returns the saved template, or null. */
export async function saveWorkspaceAsLayoutTemplate(workspaceId: string): Promise<LayoutTemplate | null> {
  const { pushToast } = useUi.getState();
  const workspace = useUi.getState().workspaces.find((candidate) => candidate.id === workspaceId);
  if (!workspace) return null;
  if (getLayoutTemplates().length >= LAYOUT_TEMPLATE_LIMITS.count) {
    pushToast("error", t("最多保存 {count} 个布局模板", { count: LAYOUT_TEMPLATE_LIMITS.count }));
    return null;
  }
  const precheck = captureLayoutTemplate(workspace, t("布局"));
  if ("error" in precheck) {
    pushToast("error", precheck.error);
    return null;
  }
  const epoch = useUi.getState().accountEpoch;
  const name = await promptText(t("布局模板名称"), [...workspace.title].slice(0, LAYOUT_TEMPLATE_LIMITS.nameChars).join(""));
  if (name === null || useUi.getState().accountEpoch !== epoch) return null;
  const latest = useUi.getState().workspaces.find((candidate) => candidate.id === workspaceId) ?? workspace;
  const captured = captureLayoutTemplate(latest, name);
  if ("error" in captured) {
    pushToast("error", captured.error);
    return null;
  }
  try {
    await addLayoutTemplate(captured.template);
  } catch (cause) {
    pushToast("error", t("保存布局模板失败：{value}", { value: describeError(cause) }));
    return null;
  }
  const paneCount = layoutTemplatePanes(captured.template.layout).length;
  pushToast("success", t("已保存布局模板「{name}」 · {paneCount} 个窗格", { name: captured.template.name, paneCount }), {
    label: t("管理模板"),
    onSelect: openLayoutTemplateSettings,
  });
  return captured.template;
}
