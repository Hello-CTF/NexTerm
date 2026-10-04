/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { probeBatch: vi.fn() };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, assetApi: { ...actual.assetApi, probeBatch: mocks.probeBatch } };
});

import {
  probeAssetReachability,
  useAssetReachability,
} from "../../features/explorer/assetReachability";

interface ProbeResult {
  assetId: string;
  reachable: boolean;
  error?: string;
  durationMs: number;
}

interface BatchCall {
  ids: string[];
  resolve: (value: { results: ProbeResult[] }) => void;
  reject: (error: unknown) => void;
}

let calls: BatchCall[] = [];

function result(
  id: string,
  reachable: boolean,
  extra: { error?: string; durationMs?: number } = {},
): ProbeResult {
  return {
    assetId: id,
    reachable,
    durationMs: extra.durationMs ?? 5,
    ...(extra.error === undefined ? {} : { error: extra.error }),
  };
}

function probe(assetIds: string[]): Promise<void> {
  return useAssetReachability.getState().probe(assetIds);
}

function entryOf(id: string) {
  return useAssetReachability.getState().entries[id];
}

beforeEach(() => {
  vi.clearAllMocks();
  calls = [];
  useAssetReachability.getState().reset();
  mocks.probeBatch.mockImplementation(
    (ids: string[]) =>
      new Promise<{ results: ProbeResult[] }>((resolve, reject) => {
        calls.push({ ids, resolve, reject });
      }),
  );
});

describe("assetReachability 重叠批次", () => {
  it("a/b 两批重叠各自成功，互不作废也不卡 checking", async () => {
    const p1 = probe(["a"]);
    const p2 = probe(["b"]);
    expect(mocks.probeBatch).toHaveBeenCalledTimes(2);
    expect(entryOf("a")?.state).toBe("checking");
    expect(entryOf("b")?.state).toBe("checking");

    calls[0].resolve({ results: [result("a", true)] });
    calls[1].resolve({ results: [result("b", true)] });
    await Promise.all([p1, p2]);

    expect(entryOf("a")?.state).toBe("reachable");
    expect(entryOf("b")?.state).toBe("reachable");
    expect(useAssetReachability.getState().batchError).toBeNull();
  });

  it("同资产后批覆盖前批，前批迟到的响应被丢弃", async () => {
    const p1 = probe(["a"]);
    const p2 = probe(["a"]);
    expect(mocks.probeBatch).toHaveBeenCalledTimes(2);

    calls[1].resolve({ results: [result("a", false, { error: "down" })] });
    await p2;
    calls[0].resolve({ results: [result("a", true)] });
    await p1;

    expect(entryOf("a")?.state).toBe("unreachable");
    expect(entryOf("a")?.error).toBe("down");
  });

  it("批次失败清理自身资产的 checking 并允许重新探测", async () => {
    const p1 = probe(["a", "b"]);
    calls[0].reject({ code: "io", message: "batch down" });
    await p1;

    expect(entryOf("a")).toBeUndefined();
    expect(entryOf("b")).toBeUndefined();
    expect(useAssetReachability.getState().batchError).toContain("batch down");

    const p2 = probe(["a"]);
    calls[1].resolve({ results: [result("a", true)] });
    await p2;
    expect(entryOf("a")?.state).toBe("reachable");
    expect(useAssetReachability.getState().batchError).toBeNull();
  });

  it("重叠批次失败只清自己的资产，不动已被新批次接管的资产", async () => {
    const p1 = probe(["a", "b"]);
    const p2 = probe(["b"]);
    calls[1].resolve({ results: [result("b", true)] });
    await p2;
    calls[0].reject({ code: "io", message: "batch down" });
    await p1;

    expect(entryOf("b")?.state).toBe("reachable");
    expect(entryOf("a")).toBeUndefined();
  });

  it("请求参数有界：timeoutMs 2500、maxConcurrent 8、去重并截断 32", async () => {
    const ids = Array.from({ length: 40 }, (_, i) => `asset-${i}`);
    const p1 = probe([...ids, "asset-0", "asset-1"]);
    expect(calls[0].ids).toHaveLength(32);
    expect(calls[0].ids).toEqual(ids.slice(0, 32));
    expect(mocks.probeBatch).toHaveBeenCalledWith(
      ids.slice(0, 32),
      2500,
      8,
    );
    calls[0].resolve({ results: [] });
    await p1;
  });
});

describe("probeAssetReachability 共享在途请求", () => {
  it("自动批次在途时手动探测等待同一请求，不重复发起", async () => {
    const p1 = probe(["a", "b"]);
    const manual = probeAssetReachability("a");
    await Promise.resolve();
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);

    calls[0].resolve({ results: [result("a", true), result("b", false, { error: "x" })] });
    await p1;

    await expect(manual).resolves.toMatchObject({ state: "reachable" });
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);
    expect(entryOf("b")?.state).toBe("unreachable");
  });

  it("手动探测赶上同资产连续两批时等到最终一批的结果", async () => {
    const p1 = probe(["a"]);
    const p2 = probe(["a"]);
    const manual = probeAssetReachability("a");

    calls[1].resolve({ results: [result("a", false, { error: "still down" })] });
    calls[0].resolve({ results: [result("a", true)] });
    await Promise.all([p1, p2]);

    await expect(manual).resolves.toMatchObject({ state: "unreachable" });
    expect(entryOf("a")?.state).toBe("unreachable");
  });

  it("无在途时手动探测自行发起并返回结果", async () => {
    const manual = probeAssetReachability("a");
    await Promise.resolve();
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);
    expect(mocks.probeBatch).toHaveBeenCalledWith(["a"], 2500, 8);
    calls[0].resolve({ results: [result("a", true, { durationMs: 31 })] });
    await expect(manual).resolves.toMatchObject({ state: "reachable", durationMs: 31 });
  });

  it("在途批次失败时手动探测返回 null 而不是卡住的 checking", async () => {
    const p1 = probe(["a", "b"]);
    const manual = probeAssetReachability("a");
    calls[0].reject({ code: "io", message: "batch down" });
    await p1;
    await expect(manual).resolves.toBeNull();
    expect(entryOf("a")).toBeUndefined();
  });
});
