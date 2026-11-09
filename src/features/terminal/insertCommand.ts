import { terminalApi } from "../../ipc/commands";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { insertRisk } from "../explorer/snippetInsert";
import { classifyAssetProduction } from "../production/productionSettings";
import { t } from "../../i18n";

export interface CommandInsertTarget {
  // Kernel terminal tab the text is written to.
  tabId: string;
  // Host name shown in the production confirmation.
  hostLabel: string;
  // Set when the terminal's host is classified as a production environment.
  productionEnvironment: string | null;
}

type WorkspaceSelection = { workspaces: Workspace[]; activeWorkspaceId: string | null };

// activeTerminalTab is the terminal tab of the active pane in the active workspace, as terminalTarget() picks it.
function activeTerminalTab(state: WorkspaceSelection): AppTab | null {
  const ws =
    state.workspaces.find((w) => w.id === state.activeWorkspaceId) ?? state.workspaces[state.workspaces.length - 1] ?? null;
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tab = pane?.tabs.find((candidate) => candidate.id === pane.activeTabId) ?? null;
  if (!tab || tab.kind !== "terminal" || !tab.tabId || tab.dead || tab.exited) return null;
  return tab;
}

// selectHasActiveTerminal is a store selector: whether text can be inserted into the active terminal right now.
export function selectHasActiveTerminal(state: WorkspaceSelection): boolean {
  return activeTerminalTab(state) !== null;
}

export function activeTerminalInsertTarget(): CommandInsertTarget | null {
  const state = useUi.getState();
  const tab = activeTerminalTab(state);
  if (!tab?.tabId) return null;
  const session = state.sessions.find((item) => item.id === tab.sessionId);
  const production = classifyAssetProduction(session?.assetId ?? tab.assetId);
  return {
    tabId: tab.tabId,
    hostLabel: session?.name ?? tab.title,
    productionEnvironment: production?.production ? production.environment : null,
  };
}

// insertCommandAtPrompt types a command at the terminal prompt without a newline, so the user reviews it before
// running it. Like the command history search, text with line breaks or control characters is never typed (it
// would run or change terminal state); the user can copy it instead. A production host always asks first.
export async function insertCommandAtPrompt(target: CommandInsertTarget, rawCommand: string): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  const command = rawCommand.replace(/[\r\n]+$/, "");
  if (!command.trim()) {
    pushToast("info", t("没有可插入的命令"));
    return false;
  }
  if (insertRisk(command) !== "none") {
    pushToast("info", t("命令包含换行或控制字符，未填入终端"), {
      label: t("复制命令"),
      onSelect: () => {
        void copyText(command).catch((cause: unknown) => {
          useUi.getState().pushToast("error", t("复制失败：{value}", { value: describeError(cause) }));
        });
      },
    });
    return false;
  }
  if (target.productionEnvironment !== null) {
    const ok = await ask(
      t("「{host}」属于生产环境「{environment}」。确定把这条命令填入它的终端吗？（不会自动执行）\n{command}", {
        host: target.hostLabel,
        environment: target.productionEnvironment,
        command,
      }),
      { kind: "warning" },
    );
    if (!ok) return false;
  }
  try {
    await terminalApi.write(target.tabId, new TextEncoder().encode(command));
    return true;
  } catch (cause) {
    pushToast("error", t("填入终端失败：{value}", { value: describeError(cause) }));
    return false;
  }
}
