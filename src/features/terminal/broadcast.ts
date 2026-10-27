import type {
  AppTab,
  BroadcastTargetReport,
  Workspace,
} from "../../app/store";
import type {
  SessionInfo,
  TerminalBroadcastWriteOptions,
} from "../../ipc/commands";
import {
  getProductionChallenge,
  type BroadcastEnvironmentConfirmation,
  type BroadcastProductionChallenge,
} from "../production/productionChallenge";

export interface BroadcastCandidate {
  storeTabId: string;
  paneId: string;
  workspaceId: string;
  workspaceTitle: string;
  hostKey: string;
  hostName: string;
  kernelTabId: string | undefined;
  title: string;
  sessionId: string;
  sessionKind: string | undefined;
  local: boolean;
  eligible: boolean;
  ineligibleReason: string | null;
}

export function isBroadcastCandidateTab(tab: AppTab): boolean {
  return tab.kind === "terminal" && typeof tab.sessionId === "string";
}

function candidateOf(
  tab: AppTab,
  paneId: string,
  ws: Workspace,
  session: SessionInfo | undefined,
): BroadcastCandidate {
  const sessionKind = session?.kind ?? ws.assetKind;
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
  } else if (!session || session.status !== "connected") {
    eligible = false;
    ineligibleReason = "主机未连接";
  } else if (!tab.tabId) {
    eligible = false;
    ineligibleReason = "终端尚未连接";
  }
  return {
    storeTabId: tab.id,
    paneId,
    workspaceId: ws.id,
    workspaceTitle: ws.title,
    hostKey: session?.assetId ?? ws.assetId ?? session?.id ?? ws.sessionId ?? ws.id,
    hostName: session?.name ?? ws.title,
    kernelTabId: tab.tabId,
    title: tab.title,
    sessionId: tab.sessionId as string,
    sessionKind,
    local: sessionKind === "local",
    eligible,
    ineligibleReason,
  };
}

export function collectBroadcastCandidates(
  workspaces: readonly Workspace[],
  sessions: readonly SessionInfo[],
): BroadcastCandidate[] {
  const sessionById = new Map(sessions.map((session) => [session.id, session]));
  const out: BroadcastCandidate[] = [];
  for (const ws of workspaces) {
    const workspaceSession = ws.sessionId ? sessionById.get(ws.sessionId) : undefined;
    for (const pane of ws.panes) {
      for (const tab of pane.tabs) {
        if (!isBroadcastCandidateTab(tab)) continue;
        const session = sessionById.get(tab.sessionId as string) ?? workspaceSession;
        out.push(candidateOf(tab, pane.id, ws, session));
      }
    }
  }
  return out;
}

export interface BroadcastWorkspaceGroup {
  workspaceId: string;
  workspaceTitle: string;
  candidates: BroadcastCandidate[];
}

export interface BroadcastHostGroup {
  hostKey: string;
  hostName: string;
  workspaces: BroadcastWorkspaceGroup[];
}

export function groupBroadcastCandidates(
  candidates: readonly BroadcastCandidate[],
): BroadcastHostGroup[] {
  const hosts = new Map<string, BroadcastHostGroup>();
  const workspaces = new Map<string, BroadcastWorkspaceGroup>();
  for (const candidate of candidates) {
    let host = hosts.get(candidate.hostKey);
    if (!host) {
      host = { hostKey: candidate.hostKey, hostName: candidate.hostName, workspaces: [] };
      hosts.set(candidate.hostKey, host);
    }
    const workspaceKey = JSON.stringify([candidate.hostKey, candidate.workspaceId]);
    let workspace = workspaces.get(workspaceKey);
    if (!workspace) {
      workspace = {
        workspaceId: candidate.workspaceId,
        workspaceTitle: candidate.workspaceTitle,
        candidates: [],
      };
      workspaces.set(workspaceKey, workspace);
      host.workspaces.push(workspace);
    }
    workspace.candidates.push(candidate);
  }
  return [...hosts.values()];
}

export function quickBroadcastIds(
  candidates: readonly BroadcastCandidate[],
  scope: "pane" | "all",
  paneId?: string,
): string[] {
  return candidates
    .filter((candidate) => candidate.eligible && (scope === "all" || candidate.paneId === paneId))
    .map((candidate) => candidate.storeTabId);
}

export interface LocalRemoteMix {
  local: number;
  remote: number;
  mixed: boolean;
}

export function localRemoteMix(candidates: readonly BroadcastCandidate[]): LocalRemoteMix {
  const local = candidates.filter((candidate) => candidate.local).length;
  const remote = candidates.length - local;
  return { local, remote, mixed: local > 0 && remote > 0 };
}

export function broadcastRiskMessage(count: number, mix: LocalRemoteMix): string {
  const base = [
    `命令广播会把你在广播来源终端里的每一次输入（含回车、粘贴与控制键）同时发送到 ${count} 个终端。`,
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

export interface BroadcastTargetResult extends BroadcastTargetReport {
  targetId: string;
  title: string;
}

export interface BroadcastOutcome {
  sent: number;
  skipped: BroadcastSkip[];
  failed: BroadcastSkip[];
  results: BroadcastTargetResult[];
}

export type BroadcastWrite = (
  kernelTabId: string,
  bytes: Uint8Array,
  options?: TerminalBroadcastWriteOptions,
) => Promise<void>;
export type BroadcastPrepare = (targetTabIds: readonly string[], bytes: Uint8Array) => Promise<void>;

export interface BroadcastWriteArgs {
  candidates: readonly BroadcastCandidate[];
  targetIds: readonly string[];
  data: string;
  write: BroadcastWrite;
  prepare?: BroadcastPrepare;
  productionConfirmation?: BroadcastEnvironmentConfirmation;
}

export function getBroadcastConfirmationChallenge(cause: unknown): BroadcastProductionChallenge | null {
  return getProductionChallenge(cause, "broadcast_production_confirmation");
}

function skipped(targetId: string, title: string, reason: string): BroadcastTargetResult {
  return { targetId, title, status: "skipped", reason };
}

function unavailableTarget(
  target: BroadcastCandidate | undefined,
  targetId: string,
): BroadcastTargetResult | null {
  if (!target) return skipped(targetId, targetId, "标签已关闭");
  if (!target.eligible) {
    return skipped(targetId, target.title, target.ineligibleReason ?? "不可广播");
  }
  if (!target.kernelTabId) return skipped(targetId, target.title, "终端尚未连接");
  return null;
}

function broadcastTargetTabIds(
  candidates: ReadonlyMap<string, BroadcastCandidate>,
  targetIds: readonly string[],
): string[] {
  const result: string[] = [];
  const seen = new Set<string>();
  for (const targetId of targetIds) {
    const target = candidates.get(targetId);
    if (unavailableTarget(target, targetId) || !target?.kernelTabId || seen.has(target.kernelTabId)) continue;
    seen.add(target.kernelTabId);
    result.push(target.kernelTabId);
  }
  return result;
}

function broadcastWriteOptions(
  targetTabIds: string[],
  confirmation: BroadcastEnvironmentConfirmation | undefined,
): TerminalBroadcastWriteOptions {
  return { targetTabIds, ...(confirmation ? { productionConfirmation: confirmation } : {}) };
}

async function writeTarget(
  target: BroadcastCandidate | undefined,
  targetId: string,
  bytes: Uint8Array,
  write: BroadcastWrite,
  options: TerminalBroadcastWriteOptions,
): Promise<BroadcastTargetResult> {
  const unavailable = unavailableTarget(target, targetId);
  if (unavailable) return unavailable;
  if (!target?.kernelTabId) return skipped(targetId, targetId, "终端尚未连接");
  try {
    await write(target.kernelTabId, bytes, options);
    return { targetId, title: target.title, status: "sent", reason: null };
  } catch (error) {
    const code = (error as { code?: string } | null)?.code;
    if (code === "needs_confirm") throw error;
    return { targetId, title: target.title, status: "failed", reason: "写入失败" };
  }
}

function throwBroadcastConfirmationRejection(
  settled: readonly PromiseSettledResult<BroadcastTargetResult>[],
): void {
  for (const result of settled) {
    if (result.status === "rejected" && (result.reason as { code?: string } | null)?.code === "needs_confirm") {
      throw result.reason;
    }
  }
}

function summarizeBroadcast(results: BroadcastTargetResult[]): BroadcastOutcome {
  return {
    sent: results.filter((result) => result.status === "sent").length,
    skipped: results
      .filter((result) => result.status === "skipped")
      .map((result) => ({ title: result.title, reason: result.reason ?? "已跳过" })),
    failed: results
      .filter((result) => result.status === "failed")
      .map((result) => ({ title: result.title, reason: result.reason ?? "写入失败" })),
    results,
  };
}

export async function broadcastInput(args: BroadcastWriteArgs): Promise<BroadcastOutcome> {
  const bytes = new TextEncoder().encode(args.data);
  const candidates = new Map(args.candidates.map((candidate) => [candidate.storeTabId, candidate]));
  const targetIds = [...new Set(args.targetIds)];
  const targetTabIds = broadcastTargetTabIds(candidates, targetIds);
  if (args.prepare && targetTabIds.length > 0) await args.prepare(targetTabIds, bytes);
  const options = broadcastWriteOptions(targetTabIds, args.productionConfirmation);
  const settled = await Promise.allSettled(
    targetIds.map((targetId) => writeTarget(candidates.get(targetId), targetId, bytes, args.write, options)),
  );
  throwBroadcastConfirmationRejection(settled);
  return summarizeBroadcast(
    settled.map((result, index) => {
      const targetId = targetIds[index] ?? "";
      if (result.status === "fulfilled") return result.value;
      return {
        targetId,
        title: candidates.get(targetId)?.title ?? targetId,
        status: "failed",
        reason: "写入失败",
      };
    }),
  );
}

export const BROADCAST_TARGET_QUEUE_LIMIT = 64;

interface BroadcastQueueWriteArgs extends BroadcastWriteArgs {
  revision: number;
  isCurrent?: () => boolean;
  resolveTarget?: (targetId: string) => BroadcastCandidate | undefined;
  onProgress?: (result: BroadcastTargetResult) => void;
}

interface TargetChain {
  tail: Promise<void>;
  revision: number;
  epoch: number;
  active: number;
}

export class BroadcastInputQueue {
  private readonly chains = new Map<string, TargetChain>();
  private revision: number | null = null;
  private epoch = 0;
  private disposed = false;
  private preflightTail: Promise<void> = Promise.resolve();

  constructor(private readonly limit = BROADCAST_TARGET_QUEUE_LIMIT) {
    if (!Number.isInteger(limit) || limit < 1) throw new Error("invalid broadcast queue limit");
  }

  reset(): void {
    this.revision = null;
    this.epoch += 1;
    for (const chain of this.chains.values()) chain.active = 0;
  }

  sync(revision: number | null): void {
    if (this.revision !== revision) this.reset();
  }

  activate(): void {
    this.disposed = false;
  }

  dispose(): void {
    this.reset();
    this.disposed = true;
  }

  private current(revision: number, epoch: number, isCurrent?: () => boolean): boolean {
    return !this.disposed && this.revision === revision && this.epoch === epoch && isCurrent?.() !== false;
  }

  private enqueueTarget(
    target: BroadcastCandidate | undefined,
    targetId: string,
    args: BroadcastQueueWriteArgs,
    bytes: Uint8Array,
    options: TerminalBroadcastWriteOptions,
    epoch: number,
  ): Promise<BroadcastTargetResult> {
    const unavailable = unavailableTarget(target, targetId);
    if (unavailable) {
      args.onProgress?.(unavailable);
      return Promise.resolve(unavailable);
    }
    if (!target) return Promise.resolve(skipped(targetId, targetId, "标签已关闭"));

    let chain = this.chains.get(targetId);
    if (!chain) {
      chain = { tail: Promise.resolve(), revision: args.revision, epoch, active: 0 };
      this.chains.set(targetId, chain);
    }
    if (chain.revision !== args.revision || chain.epoch !== epoch) {
      chain.revision = args.revision;
      chain.epoch = epoch;
      chain.active = 0;
    }
    if (chain.active >= this.limit) {
      const result = skipped(targetId, target.title, "目标写入积压，本次输入已丢弃");
      args.onProgress?.(result);
      return Promise.resolve(result);
    }

    chain.active += 1;
    args.onProgress?.({ targetId, title: target.title, status: "pending", reason: null });
    const targetChain = chain;
    const run = targetChain.tail.then(async () => {
      if (!this.current(args.revision, epoch, args.isCurrent)) {
        return skipped(targetId, target.title, "广播目标已变化，过期输入已丢弃");
      }
      const latest = args.resolveTarget ? args.resolveTarget(targetId) : target;
      const result = await writeTarget(latest, targetId, bytes, args.write, options);
      if (this.current(args.revision, epoch, args.isCurrent)) args.onProgress?.(result);
      return result;
    });
    const release = () => {
      if (
        targetChain.revision === args.revision &&
        targetChain.epoch === epoch &&
        this.epoch === epoch
      ) {
        targetChain.active = Math.max(0, targetChain.active - 1);
      }
    };
    targetChain.tail = run.then(release, release);
    return run;
  }

  async enqueue(args: BroadcastQueueWriteArgs): Promise<BroadcastOutcome | null> {
    if (this.disposed) return null;
    if (this.revision !== args.revision) {
      this.revision = args.revision;
      this.epoch += 1;
      for (const chain of this.chains.values()) {
        chain.revision = args.revision;
        chain.active = 0;
      }
    }
    const epoch = this.epoch;
    const bytes = new TextEncoder().encode(args.data);
    const candidates = new Map(args.candidates.map((candidate) => [candidate.storeTabId, candidate]));
    const targetIds = [...new Set(args.targetIds)];
    const targetTabIds = broadcastTargetTabIds(candidates, targetIds);
    if (args.prepare && targetTabIds.length > 0) {
      const prepare = args.prepare;
      const preflight = this.preflightTail.then(async () => {
        if (!this.current(args.revision, epoch, args.isCurrent)) return;
        await prepare(targetTabIds, bytes);
      });
      this.preflightTail = preflight.catch(() => undefined);
      await preflight;
      if (!this.current(args.revision, epoch, args.isCurrent)) return null;
    }
    const options = broadcastWriteOptions(targetTabIds, args.productionConfirmation);
    const tasks = targetIds.map((targetId) =>
      this.enqueueTarget(candidates.get(targetId), targetId, args, bytes, options, epoch),
    );
    const settled = await Promise.allSettled(tasks);
    if (!this.current(args.revision, epoch, args.isCurrent)) return null;
    throwBroadcastConfirmationRejection(settled);
    return summarizeBroadcast(
      settled.map((result, index) => {
        const targetId = targetIds[index] ?? "";
        if (result.status === "fulfilled") return result.value;
        return {
          targetId,
          title: candidates.get(targetId)?.title ?? targetId,
          status: "failed",
          reason: "写入失败",
        };
      }),
    );
  }
}

export function describeBroadcastSkips(skipped: BroadcastSkip[]): string {
  const shown = skipped.slice(0, 3).map((skip) => `「${skip.title}」${skip.reason}`);
  if (skipped.length > 3) shown.push(`另有 ${skipped.length - 3} 个`);
  return shown.join("、");
}
