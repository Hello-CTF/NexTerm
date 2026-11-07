import { WEB } from "../ipc/env";
import { t } from "../i18n";

type WailsRuntime = typeof import("@wailsio/runtime");

let runtimePromise: Promise<WailsRuntime> | null = null;

function runtime(): Promise<WailsRuntime> {
  runtimePromise ??= import("@wailsio/runtime");
  return runtimePromise;
}

// WebKit (macOS WKWebView, Safari, WebKitGTK) shows a native floating "Paste" menu whenever navigator.clipboard.read
// or readText is called. On desktop (!WEB) the native Wails clipboard reads and writes without triggering that menu.
export function isNativeWebKit(): boolean {
  if (typeof navigator === "undefined") return false;
  const ua = navigator.userAgent ?? "";
  return /AppleWebKit\//.test(ua) && !/Chrome|Chromium|Android|jsdom/i.test(ua);
}

function copyViaExecCommand(text: string): boolean {
  if (typeof document === "undefined" || typeof document.execCommand !== "function") return false;
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  try {
    textarea.focus();
    textarea.select();
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    textarea.remove();
  }
}

export async function copyText(text: string): Promise<void> {
  let firstError: unknown;
  const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
  const skipWebKitRead = !WEB && isNativeWebKit();
  if (!skipWebKitRead && clipboard && typeof clipboard.writeText === "function") {
    try {
      await clipboard.writeText(text);
      if (WEB || (await clipboard.readText()) === text) return;
      throw new Error(t("剪贴板写入失败"));
    } catch (e) {
      firstError = e;
    }
  }
  if (!WEB) {
    try {
      const { Clipboard } = await runtime();
      await Clipboard.SetText(text);
      if (await Clipboard.Text() === text) return;
      throw new Error(t("剪贴板写入失败"));
    } catch (e) {
      firstError ??= e;
    }
  }
  if (copyViaExecCommand(text)) {
    if (WEB) return;
    try {
      const { Clipboard } = await runtime();
      if ((await Clipboard.Text()) === text) return;
      throw new Error(t("剪贴板写入失败"));
    } catch (e) {
      firstError ??= e;
    }
  }
  throw firstError ?? new Error(t("剪贴板不可用"));
}

export async function readText(): Promise<string> {
  let firstError: unknown;
  const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
  const preferWails = !WEB && isNativeWebKit();
  if (preferWails) {
    try {
      const { Clipboard } = await runtime();
      return await Clipboard.Text();
    } catch (e) {
      firstError = e;
    }
  }
  if (clipboard && typeof clipboard.readText === "function") {
    try {
      return await clipboard.readText();
    } catch (e) {
      firstError ??= e;
    }
  }
  if (!WEB && !preferWails) {
    try {
      const { Clipboard } = await runtime();
      return await Clipboard.Text();
    } catch (e) {
      firstError ??= e;
    }
  }
  throw firstError ?? new Error(t("剪贴板不可用"));
}
