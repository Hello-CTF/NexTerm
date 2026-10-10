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
  authorize: () => Promise<"once" | "session" | "deny" | null>;
  onError: (error: unknown) => void;
  now?: () => number;
  errorThrottleMs?: number;
}

export function createOsc52Handler(deps: Osc52HandlerDeps): (payload: string) => void {
  const now = deps.now ?? Date.now;
  const errorThrottleMs = deps.errorThrottleMs ?? 4000;
  let lastError = Number.NEGATIVE_INFINITY;
  let granted: "session" | "deny" | null = null;
  let asking = false;
  const reportError = (e: unknown) => {
    const t = now();
    if (t - lastError >= errorThrottleMs) {
      lastError = t;
      deps.onError(e);
    }
  };
  const write = (text: string) => {
    try {
      deps.writeText(text).catch(reportError);
    } catch (e) {
      reportError(e);
    }
  };
  return (payload: string) => {
    const text = decodeOsc52Payload(payload);
    if (text === null) return;
    if (deps.enabled() || granted === "session") {
      write(text);
      return;
    }
    if (granted === "deny" || asking) return;
    asking = true;
    void deps
      .authorize()
      .then((choice) => {
        asking = false;
        if (choice === "session") granted = "session";
        else if (choice === "deny") granted = "deny";
        if (choice === "once" || choice === "session") write(text);
      })
      .catch(() => {
        asking = false;
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
