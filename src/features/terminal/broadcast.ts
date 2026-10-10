import type { AppTab, Workspace } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";

export interface BroadcastCandidate {
  storeTabId: string;
  paneId: string;
  kernelTabId: string | undefined;
  title: string;
  sessionId: string | undefined;
  sessionKind: string | undefined;
  local: boolean;
  eligible: boolean;
  ineligibleReason: string | null;
}

export function isBroadcastCandidateTab(tab: AppTab): boolean {
  return tab.kind === "terminal" && typeof tab.sessionId === "string";
}

function candidateOf(tab: AppTab, paneId: string, sessions: SessionInfo[]): BroadcastCandidate {
  const session = sessions.find((s) => s.id === tab.sessionId);
  const sessionKind = session?.kind;
  let eligible = true;
  let ineligibleReason: string | null = null;
  if (tab.dead) {
    eligible = false;
    ineligibleReason = "终端已失效";
  } else if (tab.exited) {
    eligible = false;
    ineligibleReason = "进程已退出";
  } else if (sessionKind === "winrm") {
    eligible = false;
    ineligibleReason = "WinRM 非交互终端不支持广播";
  }
  return {
    storeTabId: tab.id,
    paneId,
    kernelTabId: tab.tabId,
    title: tab.title,
    sessionId: tab.sessionId,
    sessionKind,
    local: sessionKind === "local",
    eligible,
    ineligibleReason,
  };
}

export function collectBroadcastCandidates(
  ws: Workspace,
  sessions: SessionInfo[],
): BroadcastCandidate[] {
  const out: BroadcastCandidate[] = [];
  for (const pane of ws.panes) {
    for (const tab of pane.tabs) {
      if (isBroadcastCandidateTab(tab)) out.push(candidateOf(tab, pane.id, sessions));
    }
  }
  return out;
}

export function quickBroadcastIds(
  candidates: BroadcastCandidate[],
  scope: "pane" | "workspace",
  paneId?: string,
): string[] {
  return candidates
    .filter((c) => c.eligible && (scope === "workspace" || c.paneId === paneId))
    .map((c) => c.storeTabId);
}

export interface LocalRemoteMix {
  local: number;
  remote: number;
  mixed: boolean;
}

export function localRemoteMix(candidates: BroadcastCandidate[]): LocalRemoteMix {
  const local = candidates.filter((c) => c.local).length;
  const remote = candidates.length - local;
  return { local, remote, mixed: local > 0 && remote > 0 };
}

export function broadcastRiskMessage(count: number, mix: LocalRemoteMix): string {
  const base = [
    `命令广播会把你在广播终端里的每一次输入（含回车、粘贴与控制键）同时发送到 ${count} 个终端。`,
    "在多台主机上同时执行同一条命令可能造成数据损坏或服务中断，请确认目标后再开启",
  ];
  if (mix.mixed) {
    base.push(
      `注意：目标同时包含本地终端（${mix.local} 个）与远程终端（${mix.remote} 个），同一条命令会在本机和远程主机上同时执行。`,
    );
  }
  return base.join("\n");
}

export interface BroadcastSkip {
  title: string;
  reason: string;
}

export interface BroadcastOutcome {
  sent: number;
  skipped: BroadcastSkip[];
}

export interface BroadcastWriteArgs {
  candidates: BroadcastCandidate[];
  targetIds: string[];
  data: string;
  write: (kernelTabId: string, bytes: Uint8Array) => Promise<void>;
}

export async function broadcastInput(args: BroadcastWriteArgs): Promise<BroadcastOutcome> {
  const bytes = new TextEncoder().encode(args.data);
  const outcome: BroadcastOutcome = { sent: 0, skipped: [] };
  for (const id of args.targetIds) {
    const target = args.candidates.find((c) => c.storeTabId === id);
    if (!target) {
      outcome.skipped.push({ title: id, reason: "标签已关闭" });
      continue;
    }
    if (!target.eligible) {
      outcome.skipped.push({ title: target.title, reason: target.ineligibleReason ?? "不可广播" });
      continue;
    }
    if (!target.kernelTabId) {
      outcome.skipped.push({ title: target.title, reason: "终端尚未连接" });
      continue;
    }
    try {
      await args.write(target.kernelTabId, bytes);
      outcome.sent += 1;
    } catch (e) {
      const code = (e as { code?: string } | null)?.code;
      outcome.skipped.push({
        title: target.title,
        reason: code === "not_controller" ? "正被其他设备控制" : "写入失败",
      });
    }
  }
  return outcome;
}

export function describeBroadcastSkips(skipped: BroadcastSkip[]): string {
  const shown = skipped.slice(0, 3).map((s) => `「${s.title}」${s.reason}`);
  if (skipped.length > 3) shown.push(`等 ${skipped.length} 个`);
  return shown.join("、");
}
