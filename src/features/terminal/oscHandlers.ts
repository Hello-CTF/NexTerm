export function decodeOsc52Payload(payload: string): string | null {
  const semi = payload.indexOf(";");
  const b64 = (semi >= 0 ? payload.slice(semi + 1) : payload).replace(/\s+/g, "");
  if (!b64) return null;
  try {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return new TextDecoder("utf-8", { fatal: false }).decode(bytes);
  } catch {
    return null;
  }
}

export interface Osc52HandlerDeps {
  enabled: () => boolean;
  writeText: (text: string) => Promise<void>;
  onDenied: () => void;
  onError: (error: unknown) => void;
  now?: () => number;
  deniedThrottleMs?: number;
  errorThrottleMs?: number;
}

export function createOsc52Handler(deps: Osc52HandlerDeps): (payload: string) => void {
  const now = deps.now ?? Date.now;
  const deniedThrottleMs = deps.deniedThrottleMs ?? 4000;
  const errorThrottleMs = deps.errorThrottleMs ?? 4000;
  let lastDenied = Number.NEGATIVE_INFINITY;
  let lastError = Number.NEGATIVE_INFINITY;
  return (payload: string) => {
    const text = decodeOsc52Payload(payload);
    if (text === null) return;
    if (!deps.enabled()) {
      const t = now();
      if (t - lastDenied >= deniedThrottleMs) {
        lastDenied = t;
        deps.onDenied();
      }
      return;
    }
    deps.writeText(text).catch((e: unknown) => {
      const t = now();
      if (t - lastError >= errorThrottleMs) {
        lastError = t;
        deps.onError(e);
      }
    });
  };
}

export interface Osc9NotifierDeps {
  notify: (body: string) => void;
  now?: () => number;
  throttleMs?: number;
}

export function createOsc9Notifier(deps: Osc9NotifierDeps): (body: string) => void {
  const now = deps.now ?? Date.now;
  const throttleMs = deps.throttleMs ?? 1500;
  let last = Number.NEGATIVE_INFINITY;
  return (body: string) => {
    const text = body.replace(/\s+/g, " ").trim().slice(0, 200).trim();
    if (!text) return;
    const t = now();
    if (t - last < throttleMs) return;
    last = t;
    deps.notify(text);
  };
}
