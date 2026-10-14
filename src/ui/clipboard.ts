import { WEB } from "../ipc/env";

type WailsRuntime = typeof import("@wailsio/runtime");

let runtimePromise: Promise<WailsRuntime> | null = null;

function runtime(): Promise<WailsRuntime> {
  runtimePromise ??= import("@wailsio/runtime");
  return runtimePromise;
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
  if (clipboard && typeof clipboard.writeText === "function") {
    try {
      await clipboard.writeText(text);
      return;
    } catch (e) {
      firstError = e;
    }
  }
  if (!WEB) {
    try {
      const { Clipboard } = await runtime();
      await Clipboard.SetText(text);
      return;
    } catch (e) {
      firstError ??= e;
    }
  }
  if (copyViaExecCommand(text)) return;
  throw firstError ?? new Error("剪贴板不可用");
}

export async function readText(): Promise<string> {
  let firstError: unknown;
  const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
  if (clipboard && typeof clipboard.readText === "function") {
    try {
      return await clipboard.readText();
    } catch (e) {
      firstError = e;
    }
  }
  if (!WEB) {
    try {
      const { Clipboard } = await runtime();
      return await Clipboard.Text();
    } catch (e) {
      firstError ??= e;
    }
  }
  throw firstError ?? new Error("剪贴板不可用");
}
