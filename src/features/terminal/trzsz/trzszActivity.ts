// Terminals (by kernel tab id) with a trzsz transfer running on this device.
// Broadcast skips them: typed input would corrupt the transfer stream.
const active = new Set<string>();

export function setTrzszTransferActive(tabId: string, on: boolean): void {
  if (!tabId) return;
  if (on) active.add(tabId);
  else active.delete(tabId);
}

export function isTrzszTransferActive(tabId: string | undefined): boolean {
  return tabId !== undefined && active.has(tabId);
}
