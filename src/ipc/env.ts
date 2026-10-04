
export type Transport = "desktop" | "web" | "demo";

function detect(): Transport {
  if (typeof window === "undefined") return "desktop";
  const w = window as unknown as Record<string, unknown>;
  const marker = w.__NEXTERM_TRANSPORT__;
  const isWails = marker === "desktop" || w.wails !== undefined;
  const isWeb = marker === "web";

  let flag: string | null = null;
  try {
    flag = new URLSearchParams(window.location.search).get("demo");
  } catch {
  }

  if (flag === "1") return "demo";
  if (isWails) return "desktop";
  if (isWeb) return "web";
  return "demo";
}

export const TRANSPORT: Transport = detect();

export const DEMO = TRANSPORT === "demo";

export const WEB = TRANSPORT === "web";

export const DESKTOP = TRANSPORT === "desktop";

const CLIENT_KEY = "nexterm.client";

let memoryClientId = "";

export function clientId(): string {
  try {
    let v = window.localStorage.getItem(CLIENT_KEY);
    if (!v) {
      v = newClientId();
      window.localStorage.setItem(CLIENT_KEY, v);
    }
    return v;
  } catch {
    if (!memoryClientId) memoryClientId = newClientId();
    return memoryClientId;
  }
}

function newClientId(): string {
  return `d${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
}

export function apiBase(): string {
  if (typeof window === "undefined") return "";
  try {
    const fromQuery = new URLSearchParams(window.location.search).get("api");
    if (fromQuery) return fromQuery.replace(/\/$/, "");
    const fromStore = window.localStorage.getItem("nexterm.api");
    if (fromStore) return fromStore.replace(/\/$/, "");
  } catch {
  }
  return "";
}

export function httpUrl(path: string): string {
  const base = apiBase();
  return base ? `${base}${path}` : path;
}

export function wsUrl(path: string): string {
  const base = apiBase();
  if (base) {
    return `${base.replace(/^http/, "ws")}${path}`;
  }
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}${path}`;
}
