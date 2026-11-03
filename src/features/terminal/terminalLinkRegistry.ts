import type { ExtractedLink } from "./terminalLinkProvider";

export interface TerminalLinkSource {
  extract(): ExtractedLink[];
  open(url: string): void;
  openPanel(): void;
}

const sources = new Map<string, TerminalLinkSource>();

// Lets the jump palette reach the links of a mounted terminal. Returns a function that
// removes only this registration, so a replaced source is not dropped by its predecessor.
export function registerTerminalLinkSource(storeTabId: string, source: TerminalLinkSource): () => void {
  sources.set(storeTabId, source);
  return () => {
    if (sources.get(storeTabId) === source) sources.delete(storeTabId);
  };
}

export function terminalLinkSource(storeTabId: string): TerminalLinkSource | undefined {
  return sources.get(storeTabId);
}
