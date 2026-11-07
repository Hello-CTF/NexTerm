import { planTerminalLink } from "../terminal/terminalLinks";
import { t } from "../../i18n";

export interface EmbedEnv {
  appOrigin: string;
  web: boolean;
  mobile: boolean;
}

export type EmbedPlan =
  | { kind: "embed"; url: string }
  | { kind: "external"; url: string; reason: string }
  | { kind: "blocked"; reason: string };

// Windows/Android serve app assets from http(s)://wails.localhost, and WebView2 intercepts by host prefix, so the
// match is a lowercase, trailing-dot-trimmed prefix. Linux/macOS/iOS use wails://localhost, a scheme that is never
// same-origin with http(s), so a plain http://localhost preview stays allowed.
function isWailsAssetHost(hostname: string): boolean {
  const host = hostname.toLowerCase().replace(/\.+$/, "");
  return host.startsWith("wails.localhost") || host === "wails";
}

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
  if (plan.kind !== "external") return { kind: "blocked", reason: t("只能打开 http 或 https 链接") };
  const parsed = parse(plan.url);
  if (!parsed || (parsed.protocol !== "http:" && parsed.protocol !== "https:")) {
    return { kind: "blocked", reason: t("只能打开 http 或 https 链接") };
  }
  if (parsed.origin === env.appOrigin) return { kind: "blocked", reason: t("不能在应用内嵌入 NexTerm 自身的页面") };
  if (isWailsAssetHost(parsed.hostname)) {
    return { kind: "blocked", reason: t("不能在应用内嵌入 NexTerm 自身的页面") };
  }
  if (env.mobile) return { kind: "external", url: plan.url, reason: t("移动端不支持应用内打开") };
  if (env.web && parsed.protocol === "http:") {
    return { kind: "external", url: plan.url, reason: t("浏览器模式只能嵌入 https 页面") };
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
