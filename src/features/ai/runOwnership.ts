import { GLOBAL_AI_BOARD_KEY, useUi, type AiDockTab, type WorkspaceKind } from "../../app/store";

export type AiRunKind = "chat" | "takeover";
export type AiRunCancellation = "idle" | "cancelling" | "unconfirmed";

export const aiTabBusyIds = new Set<string>();
export const aiTabRunControls = new Map<string, () => Promise<boolean>>();
const aiTabBusyListeners = new Set<() => void>();
let aiTabBusyVersion = 0;

export function setAiTabBusy(tabKey: string, busy: boolean): void {
  if (aiTabBusyIds.has(tabKey) === busy) return;
  if (busy) aiTabBusyIds.add(tabKey);
  else aiTabBusyIds.delete(tabKey);
  aiTabBusyVersion += 1;
  const aiBusy = Object.values(useUi.getState().aiBoards).some((board) =>
    board.tabs.some((tab) => aiTabBusyIds.has(tab.id)),
  );
  useUi.getState().setAiBusy(aiBusy);
  for (const listener of [...aiTabBusyListeners]) listener();
}

export function subscribeAiTabBusy(listener: () => void): () => void {
  aiTabBusyListeners.add(listener);
  return () => {
    aiTabBusyListeners.delete(listener);
  };
}

export function getAiTabBusyVersion(): number {
  return aiTabBusyVersion;
}

export async function confirmAiRunCancellation(
  cancelAndConfirm: () => Promise<boolean>,
  timeoutMs = 5000,
): Promise<boolean> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      cancelAndConfirm(),
      new Promise<boolean>((resolve) => {
        timer = setTimeout(() => resolve(false), timeoutMs);
      }),
    ]);
  } catch {
    return false;
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

export type AiCloseTab = AiDockTab & { generation?: number };

const aiTabRunGenerations = new Map<string, number>();

export function registerAiRunGeneration(tabKey: string, generation: number): void {
  aiTabRunGenerations.set(tabKey, generation);
}

export function clearAiTabRun(tabKey: string): void {
  aiTabBusyIds.delete(tabKey);
  aiTabRunControls.delete(tabKey);
  aiTabRunGenerations.delete(tabKey);
}

export function clearAiTabRuns(tabIds: Iterable<string>): void {
  for (const tabId of tabIds) clearAiTabRun(tabId);
}

export function runningAiTabs(workspaceKind: WorkspaceKind, workspaceId: string): AiCloseTab[] {
  const boardKey = workspaceKind === "tools" ? GLOBAL_AI_BOARD_KEY : workspaceId;
  const board = useUi.getState().aiBoards[boardKey];
  return (board?.tabs ?? [])
    .filter((tab) => aiTabBusyIds.has(tab.id))
    .map((tab) => ({ ...tab, generation: aiTabRunGenerations.get(tab.id) }));
}

export interface AiRunSlot {
  generation: number;
  kind: AiRunKind;
  jobId: string | null;
  spawnPending: boolean;
  settled: boolean;
  cancellation: AiRunCancellation;
}

export function createAiRun(generation: number, kind: AiRunKind = "chat"): AiRunSlot {
  return { generation, kind, jobId: null, spawnPending: true, settled: false, cancellation: "idle" };
}

export function bindAiRunJob(run: AiRunSlot, jobId: string): AiRunSlot {
  return { ...run, jobId, spawnPending: false };
}

export function finishAiRunSpawn(run: AiRunSlot): AiRunSlot {
  return { ...run, spawnPending: false };
}

export function settleAiRun(run: AiRunSlot): AiRunSlot {
  return { ...run, settled: true };
}

export function requestAiRunCancellation(run: AiRunSlot): AiRunSlot {
  return { ...run, cancellation: "cancelling" };
}

export function markAiRunCancellationUnconfirmed(run: AiRunSlot): AiRunSlot {
  return { ...run, cancellation: "unconfirmed" };
}

export function confirmAiRunTerminal(run: AiRunSlot): AiRunSlot {
  return { ...run, settled: true, cancellation: "idle" };
}

export function isCurrentAiRun(run: AiRunSlot | null, generation: number): run is AiRunSlot {
  return run?.generation === generation;
}

export function aiRunBlocksStart(run: AiRunSlot | null): boolean {
  return !!run && (!run.settled || run.spawnPending);
}
