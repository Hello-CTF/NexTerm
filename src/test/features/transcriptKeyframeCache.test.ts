/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  loadCachedKeyframes,
  saveCachedKeyframes,
  transcriptFingerprint,
} from "../../features/terminal/transcriptKeyframeCache";
import type { ReplayKeyframe } from "../../features/terminal/transcriptReplay";

interface StoredEntry {
  id: string;
  fingerprint: string;
  version: number;
  savedAt: number;
  frames: ReplayKeyframe[];
}

interface FakeRequest<T> {
  result: T;
  onsuccess: (() => void) | null;
  onerror: (() => void) | null;
}

interface FakeOpenRequest extends FakeRequest<IDBDatabase> {
  onupgradeneeded: (() => void) | null;
  onblocked: (() => void) | null;
}

function resolved<T>(result: T): IDBRequest<T> {
  const request: FakeRequest<T> = { result, onsuccess: null, onerror: null };
  queueMicrotask(() => request.onsuccess?.());
  return request as unknown as IDBRequest<T>;
}

function installIndexedDb() {
  const entries = new Map<string, StoredEntry>();
  const close = vi.fn();
  const store = {
    get: (id: string) => resolved(entries.get(id)),
    getAll: () => resolved([...entries.values()]),
    put: (entry: StoredEntry) => {
      entries.set(entry.id, entry);
      return resolved(entry.id);
    },
    delete: (id: string) => {
      entries.delete(id);
      return resolved(undefined);
    },
  };
  const db = {
    createObjectStore: () => store,
    transaction: () => ({ objectStore: () => store }),
    close,
  } as unknown as IDBDatabase;
  let upgraded = false;
  const factory = {
    open: () => {
      const request: FakeOpenRequest = {
        result: db,
        onsuccess: null,
        onerror: null,
        onupgradeneeded: null,
        onblocked: null,
      };
      queueMicrotask(() => {
        if (!upgraded) {
          upgraded = true;
          request.onupgradeneeded?.();
        }
        request.onsuccess?.();
      });
      return request as unknown as IDBOpenDBRequest;
    },
  };
  vi.stubGlobal("indexedDB", factory);
  return { close };
}

function frame(nextIndex: number): ReplayKeyframe {
  return { positionMs: nextIndex * 1000, nextIndex, cols: 80, rows: 24, state: `screen-${nextIndex}` };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("transcript keyframe cache", () => {
  it("fingerprints the recording and falls back without browser storage", async () => {
    expect(transcriptFingerprint({ chunkCount: 4, lastSeq: 9, firstTs: 100, lastTs: 900 })).toBe("4:9:100:900");
    vi.stubGlobal("indexedDB", undefined);

    await expect(loadCachedKeyframes("ws", "fp")).resolves.toBeNull();
    await expect(saveCachedKeyframes("ws", "fp", [frame(1)])).resolves.toBeUndefined();
  });

  it("round-trips matching frames and rejects stale fingerprints or missing entries", async () => {
    const { close } = installIndexedDb();
    await saveCachedKeyframes("ws", "fp", [frame(1)]);

    await expect(loadCachedKeyframes("ws", "fp")).resolves.toEqual([frame(1)]);
    await expect(loadCachedKeyframes("ws", "stale")).resolves.toBeNull();
    await expect(loadCachedKeyframes("missing", "fp")).resolves.toBeNull();
    expect(close).toHaveBeenCalledTimes(4);
  });

  it("keeps the twenty most recently saved recordings", async () => {
    installIndexedDb();
    let now = 0;
    vi.spyOn(Date, "now").mockImplementation(() => ++now);
    for (let index = 0; index <= 20; index++) {
      await saveCachedKeyframes(`ws-${index}`, "fp", [frame(index)]);
    }

    await expect(loadCachedKeyframes("ws-0", "fp")).resolves.toBeNull();
    for (let index = 1; index <= 20; index++) {
      await expect(loadCachedKeyframes(`ws-${index}`, "fp")).resolves.toEqual([frame(index)]);
    }
  });
});
