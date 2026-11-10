/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  loadCachedKeyframes,
  saveCachedKeyframes,
  transcriptFingerprint,
} from "../../features/terminal/transcriptKeyframeCache";
import type { ReplayKeyframe } from "../../features/terminal/transcriptReplay";

// A minimal in-memory IndexedDB covering the calls the cache makes. Requests settle on a microtask, like the real thing.
interface FakeRequest<T> {
  result: T;
  onsuccess: (() => void) | null;
  onerror: (() => void) | null;
  onupgradeneeded?: (() => void) | null;
  onblocked?: (() => void) | null;
}

interface FakeBehavior {
  failOpen: boolean;
  blockOpen: boolean;
  failGet: boolean;
  throwOnTransaction: boolean;
  throwOnPut: boolean;
  openCalls: number;
  closeCalls: number;
}

function installFakeIndexedDb(behavior: FakeBehavior) {
  const rows = new Map<string, { id: string; savedAt: number } & Record<string, unknown>>();
  let upgraded = false;
  function request<T>(compute: () => T, fail = false): FakeRequest<T> {
    const req: FakeRequest<T> = { result: undefined as T, onsuccess: null, onerror: null };
    queueMicrotask(() => {
      if (fail) {
        req.onerror?.();
        return;
      }
      req.result = compute();
      req.onsuccess?.();
    });
    return req;
  }
  const store = {
    get: (id: string) => request(() => rows.get(id), behavior.failGet),
    put: (value: { id: string; savedAt: number }) => {
      if (behavior.throwOnPut) throw new Error("quota");
      return request(() => {
        rows.set(value.id, value);
        return value.id;
      });
    },
    getAll: () => request(() => [...rows.values()]),
    delete: (id: string) => request(() => rows.delete(id)),
  };
  const db = {
    close: () => {
      behavior.closeCalls += 1;
    },
    createObjectStore: vi.fn(),
    transaction: () => {
      if (behavior.throwOnTransaction) throw new Error("closed");
      return { objectStore: () => store };
    },
  };
  const factory = {
    open: () => {
      behavior.openCalls += 1;
      const req = { result: db, onsuccess: null, onerror: null, onupgradeneeded: null, onblocked: null } as FakeRequest<typeof db>;
      queueMicrotask(() => {
        if (behavior.failOpen) return req.onerror?.();
        if (behavior.blockOpen) return req.onblocked?.();
        if (!upgraded) {
          upgraded = true;
          req.onupgradeneeded?.();
        }
        req.onsuccess?.();
      });
      return req;
    },
  };
  vi.stubGlobal("indexedDB", factory);
  return { rows, db };
}

const frame = (positionMs: number): ReplayKeyframe => ({ positionMs, nextIndex: positionMs, cols: 80, rows: 24, state: `s${positionMs}` });
const info = { chunkCount: 10, lastSeq: 9, firstTs: 1000, lastTs: 5000 };

let behavior: FakeBehavior;
let fake: ReturnType<typeof installFakeIndexedDb>;

beforeEach(() => {
  behavior = { failOpen: false, blockOpen: false, failGet: false, throwOnTransaction: false, throwOnPut: false, openCalls: 0, closeCalls: 0 };
  fake = installFakeIndexedDb(behavior);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("transcriptFingerprint", () => {
  it("changes when anything is appended or replaced", () => {
    const base = transcriptFingerprint(info);
    expect(transcriptFingerprint({ ...info })).toBe(base);
    for (const patch of [{ chunkCount: 11 }, { lastSeq: 10 }, { firstTs: 999 }, { lastTs: 5001 }]) {
      expect(transcriptFingerprint({ ...info, ...patch })).not.toBe(base);
    }
  });
});

describe("keyframe cache", () => {
  it("returns saved frames for the same recording and closes the database each time", async () => {
    const fingerprint = transcriptFingerprint(info);
    await saveCachedKeyframes("rec", fingerprint, [frame(0), frame(60000)]);
    expect(await loadCachedKeyframes("rec", fingerprint)).toEqual([frame(0), frame(60000)]);
    expect(behavior.closeCalls).toBe(2);
  });

  it("misses for an unknown recording or a recording that changed", async () => {
    await saveCachedKeyframes("rec", transcriptFingerprint(info), [frame(0)]);
    expect(await loadCachedKeyframes("other", transcriptFingerprint(info))).toBeNull();
    expect(await loadCachedKeyframes("rec", transcriptFingerprint({ ...info, chunkCount: 11 }))).toBeNull();
  });

  it("misses for an entry written by another cache version", async () => {
    await saveCachedKeyframes("rec", "fp", [frame(0)]);
    fake.rows.get("rec")!.version = 0;
    expect(await loadCachedKeyframes("rec", "fp")).toBeNull();
  });

  it("misses for an entry built with the Unicode 6 widths of the first format", async () => {
    await saveCachedKeyframes("rec", "fp", [frame(0)]);
    fake.rows.get("rec")!.version = 1;
    expect(await loadCachedKeyframes("rec", "fp")).toBeNull();
    await saveCachedKeyframes("rec", "fp", [frame(1)]);
    expect(await loadCachedKeyframes("rec", "fp")).toEqual([frame(1)]);
  });

  it("replaces the frames of a recording that was saved again", async () => {
    await saveCachedKeyframes("rec", "fp1", [frame(0)]);
    await saveCachedKeyframes("rec", "fp2", [frame(1), frame(2)]);
    expect(await loadCachedKeyframes("rec", "fp1")).toBeNull();
    expect(await loadCachedKeyframes("rec", "fp2")).toEqual([frame(1), frame(2)]);
    expect(fake.rows.size).toBe(1);
  });

  it("keeps only the 20 most recently saved recordings", async () => {
    vi.useFakeTimers();
    for (let index = 0; index < 23; index += 1) {
      vi.setSystemTime(1_000_000 + index * 1000);
      await saveCachedKeyframes(`rec${index}`, "fp", [frame(index)]);
    }
    expect(fake.rows.size).toBe(20);
    for (let index = 0; index < 3; index += 1) expect(fake.rows.has(`rec${index}`)).toBe(false);
    expect(await loadCachedKeyframes("rec22", "fp")).toEqual([frame(22)]);
    expect(await loadCachedKeyframes("rec3", "fp")).toEqual([frame(3)]);
  });

  it("is a miss, not an error, when IndexedDB is unavailable", async () => {
    vi.stubGlobal("indexedDB", undefined);
    expect(await loadCachedKeyframes("rec", "fp")).toBeNull();
    await expect(saveCachedKeyframes("rec", "fp", [frame(0)])).resolves.toBeUndefined();
  });

  it("is a miss when the database cannot be opened, is blocked, or open throws", async () => {
    behavior.failOpen = true;
    expect(await loadCachedKeyframes("rec", "fp")).toBeNull();
    await expect(saveCachedKeyframes("rec", "fp", [frame(0)])).resolves.toBeUndefined();
    behavior.failOpen = false;
    behavior.blockOpen = true;
    expect(await loadCachedKeyframes("rec", "fp")).toBeNull();
    vi.stubGlobal("indexedDB", {
      open: () => {
        throw new Error("denied");
      },
    });
    expect(await loadCachedKeyframes("rec", "fp")).toBeNull();
  });

  it("is a miss when the read fails or the transaction cannot start, and still closes", async () => {
    await saveCachedKeyframes("rec", "fp", [frame(0)]);
    behavior.closeCalls = 0;
    behavior.failGet = true;
    expect(await loadCachedKeyframes("rec", "fp")).toBeNull();
    behavior.failGet = false;
    behavior.throwOnTransaction = true;
    expect(await loadCachedKeyframes("rec", "fp")).toBeNull();
    expect(behavior.closeCalls).toBe(2);
  });

  it("swallows a failed write and still closes the database", async () => {
    behavior.throwOnPut = true;
    await expect(saveCachedKeyframes("rec", "fp", [frame(0)])).resolves.toBeUndefined();
    expect(fake.rows.size).toBe(0);
    expect(behavior.closeCalls).toBe(1);
    behavior.throwOnPut = false;
    behavior.throwOnTransaction = true;
    await expect(saveCachedKeyframes("rec", "fp", [frame(0)])).resolves.toBeUndefined();
  });
});
