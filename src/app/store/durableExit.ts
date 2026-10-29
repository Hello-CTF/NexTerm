import type { AppTab } from "./types";

// A durable terminal that reads as exited is not necessarily gone: a dropped link or a stopped stream also ends the
// tab's stream while the process keeps running on the host. Only an exit status or a confirmed loss is proof.

export function isDurableTab(tab: Pick<AppTab, "durable" | "retainable">): boolean {
  return tab.durable === true || (tab.durable === undefined && tab.retainable === true);
}

export function hasExitEvidence(tab: Pick<AppTab, "exitCode" | "exitConfirmed" | "durableLost">): boolean {
  return tab.durableLost === true || tab.exitConfirmed === true || typeof tab.exitCode === "number";
}

// isUnconfirmedDurableExit marks an exited durable terminal whose process may still run on the host. Closing it
// detaches (keeps it in the background sessions) instead of ending anything.
export function isUnconfirmedDurableExit(tab: AppTab): boolean {
  return tab.kind === "terminal" && !tab.dead && tab.exited === true && isDurableTab(tab) && !hasExitEvidence(tab);
}

// isConnectionLostTab marks a durable terminal that lost its attachment: confirmed lost, or exited without proof.
export function isConnectionLostTab(tab: AppTab): boolean {
  if (tab.kind !== "terminal" || tab.dead || !tab.tabId || tab.containerId) return false;
  if (!isDurableTab(tab)) return false;
  return tab.durableLost === true || isUnconfirmedDurableExit(tab);
}
