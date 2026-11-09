// Lets a snippet the user started observe the output of the terminal it runs in. Mounted terminal views
// publish the live bytes they accept and expose their screen text; nothing is buffered or decoded while no
// one listens, so the tap is never an always-on trigger.

export interface TerminalScreenSource {
  // The kernel tab the view is attached to, or "" while it is not attached.
  tabId: () => string;
  // The screen text up to the cursor.
  screenText: () => string;
}

type OutputListener = (bytes: Uint8Array) => void;

const sources = new Set<TerminalScreenSource>();
const listeners = new Map<string, Set<OutputListener>>();

export function registerTerminalScreenSource(source: TerminalScreenSource): () => void {
  sources.add(source);
  return () => {
    sources.delete(source);
  };
}

export function readTerminalScreen(tabId: string): string | null {
  for (const source of sources) {
    if (source.tabId() === tabId) return source.screenText();
  }
  return null;
}

export function publishTerminalOutput(tabId: string, bytes: Uint8Array): void {
  const set = listeners.get(tabId);
  if (!set || bytes.length === 0) return;
  for (const listener of [...set]) listener(bytes);
}

export function subscribeTerminalOutput(tabId: string, listener: OutputListener): () => void {
  let set = listeners.get(tabId);
  if (!set) {
    set = new Set();
    listeners.set(tabId, set);
  }
  set.add(listener);
  return () => {
    const current = listeners.get(tabId);
    if (!current) return;
    current.delete(listener);
    if (current.size === 0) listeners.delete(tabId);
  };
}
