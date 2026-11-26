import { openFileTab, useUi } from "../../app/store";
import { t } from "../../i18n";

// A file change the AI made, to review in the file editor: the content before the write and the content the write
// left on the host.
export interface AiFileReview {
  before: string;
  after: string;
}

// A request no editor picked up (the open failed or was cancelled) must not start a review in a later, unrelated open.
const REVIEW_TTL_MS = 30_000;
const pending = new Map<string, { review: AiFileReview; at: number }>();
const listeners = new Set<() => void>();

function key(sessionId: string, path: string): string {
  return `${sessionId}\u0000${path}`;
}

export function requestFileReview(sessionId: string, path: string, review: AiFileReview): void {
  pending.set(key(sessionId, path), { review, at: Date.now() });
  for (const listener of [...listeners]) listener();
}

/** Takes the pending review for this file, if any; each request is delivered once. */
export function takeFileReview(sessionId: string, path: string): AiFileReview | null {
  const k = key(sessionId, path);
  const entry = pending.get(k);
  pending.delete(k);
  return entry && Date.now() - entry.at <= REVIEW_TTL_MS ? entry.review : null;
}

export function subscribeFileReview(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// openAiChangeReview opens the changed file in the editor and shows the AI's change there as a diff.
export function openAiChangeReview(sessionId: string, path: string, review: AiFileReview): boolean {
  if (!useUi.getState().sessions.some((session) => session.id === sessionId)) {
    useUi.getState().pushToast("info", t("该文件所在的会话已关闭"));
    return false;
  }
  requestFileReview(sessionId, path, review);
  openFileTab(sessionId, path);
  return true;
}
