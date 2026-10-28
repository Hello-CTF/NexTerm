import { useSyncExternalStore } from "react";

// Whether this device already acknowledged that images bypass the privacy guard. Per-device, never synced.
const STORAGE_KEY = "nexterm.ai-privacy-image-notice.v1";

export const PRIVACY_IMAGE_NOTICE = "已开启 AI 信息脱敏，但图片不会脱敏，将原样发送给 AI 服务。";

let seen: boolean | null = null;
const listeners = new Set<() => void>();

function read(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === "seen";
  } catch {
    return false;
  }
}

export function isPrivacyImageNoticeSeen(): boolean {
  seen ??= read();
  return seen;
}

export function acknowledgePrivacyImageNotice(): void {
  if (isPrivacyImageNoticeSeen()) return;
  seen = true;
  try {
    localStorage.setItem(STORAGE_KEY, "seen");
  } catch {
    // Storage may be unavailable (private mode); the notice stays hidden until reload.
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function usePrivacyImageNoticeSeen(): boolean {
  return useSyncExternalStore(subscribe, isPrivacyImageNoticeSeen, isPrivacyImageNoticeSeen);
}

export function resetPrivacyImageNoticeForTest(): void {
  seen = null;
}
