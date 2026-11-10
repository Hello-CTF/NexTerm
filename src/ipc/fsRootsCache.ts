import type { FSRootsDTO } from "./generated/types";

// The last fs_roots answer per session, so code outside React (listing normalization, upload conflict checks)
// follows the target's path style and capabilities.
const rootsBySession = new Map<string, FSRootsDTO>();

export function noteFsRoots(sessionId: string, roots: FSRootsDTO): void {
  rootsBySession.set(sessionId, roots);
}

export function cachedFsRoots(sessionId: string): FSRootsDTO | undefined {
  return rootsBySession.get(sessionId);
}

// clearFsRoots forgets every session's answer when the session list is reset (account switch, logout).
export function clearFsRoots(): void {
  rootsBySession.clear();
}
