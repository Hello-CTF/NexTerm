
type Handler = (payload: unknown) => void;

const handlers = new Map<string, Set<Handler>>();

export function emit(event: string, payload: unknown) {
  const set = handlers.get(event);
  if (!set) return;
  for (const h of [...set]) {
    try {
      h(payload);
    } catch {
    }
  }
}

export function subscribe(event: string, handler: Handler): () => void {
  let set = handlers.get(event);
  if (!set) {
    set = new Set();
    handlers.set(event, set);
  }
  set.add(handler);
  return () => {
    set.delete(handler);
    if (set.size === 0) handlers.delete(event);
  };
}

export interface MockChannel {
  onmessage?: (message: unknown) => void;
}

export function pushText(channel: unknown, text: string) {
  const c = channel as MockChannel | undefined;
  c?.onmessage?.(new TextEncoder().encode(text));
}

export function pushEvent(channel: unknown, event: Record<string, unknown>) {
  const c = channel as MockChannel | undefined;
  c?.onmessage?.(event);
}

export function later(ms: number, fn: () => void): () => void {
  const t = window.setTimeout(fn, ms);
  return () => window.clearTimeout(t);
}
