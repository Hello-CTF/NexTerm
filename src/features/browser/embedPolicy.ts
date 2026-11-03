import { planTerminalLink } from "../terminal/terminalLinks";

export interface EmbedEnv {
  appOrigin: string;
  web: boolean;
  mobile: boolean;
}

export type EmbedPlan =
  | { kind: "embed"; url: string }
  | { kind: "external"; url: string; reason: string }
  | { kind: "blocked"; reason: string };

// Hosts that serve the Wails app assets (http(s)://wails.localhost on Windows/Android, wails://wails elsewhere).
const WAILS_HOSTS = new Set(["wails.localhost", "wails"]);

export const EMBED_SANDBOX = "allow-scripts allow-forms allow-same-origin";

function parse(url: string): URL | null {
  try {
    return new URL(url);
  } catch {
    return null;
  }
}

export function planEmbed(rawUrl: string, env: EmbedEnv): EmbedPlan {
  const plan = planTerminalLink(rawUrl);
  if (plan.kind !== "external") return { kind: "blocked", reason: "只能打开 http 或 https 链接" };
  const parsed = parse(plan.url);
  if (!parsed || (parsed.protocol !== "http:" && parsed.protocol !== "https:")) {
    return { kind: "blocked", reason: "只能打开 http 或 https 链接" };
  }
  if (parsed.origin === env.appOrigin) return { kind: "blocked", reason: "不能在应用内嵌入 NexTerm 自身的页面" };
  if (WAILS_HOSTS.has(parsed.hostname.toLowerCase())) {
    return { kind: "blocked", reason: "不能在应用内嵌入 NexTerm 自身的页面" };
  }
  if (env.mobile) return { kind: "external", url: plan.url, reason: "移动端不支持应用内打开" };
  if (env.web && parsed.protocol === "http:") {
    return { kind: "external", url: plan.url, reason: "浏览器模式只能嵌入 https 页面" };
  }
  return { kind: "embed", url: plan.url };
}

// A same-origin frame with allow-scripts and allow-same-origin could remove its own sandbox, so that token is
// dropped if a same-origin URL ever reaches the frame.
export function sandboxFor(url: string, appOrigin: string): string {
  const parsed = parse(url);
  if (parsed && parsed.origin === appOrigin) return EMBED_SANDBOX.replace(" allow-same-origin", "");
  return EMBED_SANDBOX;
}

export function currentEmbedEnv(web: boolean, mobile: boolean): EmbedEnv {
  return { appOrigin: typeof window === "undefined" ? "" : window.location.origin, web, mobile };
}
