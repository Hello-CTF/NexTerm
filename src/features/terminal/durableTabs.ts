const durableTabIds = new Set<string>();
const MAX_TRACKED = 512;

export function noteTabDurable(tabId: string, durable: boolean): void {
  if (!durable) {
    durableTabIds.delete(tabId);
    return;
  }
  if (durableTabIds.size >= MAX_TRACKED) {
    const oldest = durableTabIds.values().next().value;
    if (oldest !== undefined) durableTabIds.delete(oldest);
  }
  durableTabIds.add(tabId);
}

export function isTabDurable(tabId: string): boolean {
  return durableTabIds.has(tabId);
}

export function resetDurableTabs(): void {
  durableTabIds.clear();
}
