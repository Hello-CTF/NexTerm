import type { ReplayKeyframe } from "./transcriptReplay";

const DB_NAME = "nexterm-replay-keyframes";
const STORE = "frames";
// Bump when the serialized state format or the build parameters change.
const CACHE_VERSION = 1;
const MAX_ENTRIES = 20;

interface CacheEntry {
  id: string;
  fingerprint: string;
  version: number;
  savedAt: number;
  frames: ReplayKeyframe[];
}

// The fingerprint ties a cached index to the exact content it was built from; any append or replace changes it.
export function transcriptFingerprint(info: { chunkCount: number; lastSeq: number; lastTs: number; firstTs: number }): string {
  return `${info.chunkCount}:${info.lastSeq}:${info.firstTs}:${info.lastTs}`;
}

function openDb(): Promise<IDBDatabase | null> {
  return new Promise((resolve) => {
    try {
      if (typeof indexedDB === "undefined") {
        resolve(null);
        return;
      }
      const request = indexedDB.open(DB_NAME, CACHE_VERSION);
      request.onupgradeneeded = () => {
        request.result.createObjectStore(STORE, { keyPath: "id" });
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => resolve(null);
      request.onblocked = () => resolve(null);
    } catch {
      resolve(null);
    }
  });
}

function requestResult<T>(request: IDBRequest<T>): Promise<T | undefined> {
  return new Promise((resolve) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => resolve(undefined);
  });
}

export async function loadCachedKeyframes(id: string, fingerprint: string): Promise<ReplayKeyframe[] | null> {
  const db = await openDb();
  if (!db) return null;
  try {
    const entry = (await requestResult(db.transaction(STORE, "readonly").objectStore(STORE).get(id))) as
      | CacheEntry
      | undefined;
    if (!entry || entry.version !== CACHE_VERSION || entry.fingerprint !== fingerprint) return null;
    return entry.frames;
  } catch {
    return null;
  } finally {
    db.close();
  }
}

export async function saveCachedKeyframes(id: string, fingerprint: string, frames: ReplayKeyframe[]): Promise<void> {
  const db = await openDb();
  if (!db) return;
  try {
    const store = db.transaction(STORE, "readwrite").objectStore(STORE);
    await requestResult(store.put({ id, fingerprint, version: CACHE_VERSION, savedAt: Date.now(), frames } satisfies CacheEntry));
    const all = ((await requestResult(store.getAll())) as CacheEntry[] | undefined) ?? [];
    const stale = all.sort((a, b) => b.savedAt - a.savedAt).slice(MAX_ENTRIES);
    for (const entry of stale) store.delete(entry.id);
  } catch {
    // The cache is an optimization; a failed write only costs a rebuild next time.
  } finally {
    db.close();
  }
}
