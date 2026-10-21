/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  AssetProbeBatchDTO,
  AssetProbeBatchRequest,
  AssetProbeResultDTO,
} from "../../ipc/generated/types";
import type { AssetProbeBatchDto, AssetProbeResultDto } from "../../ipc/types";

type TypeEqual<Left, Right> =
  (<Type>() => Type extends Left ? 1 : 2) extends
  (<Type>() => Type extends Right ? 1 : 2) ? true : false;

if (false) {
  const resultMatchesGenerated: TypeEqual<AssetProbeResultDto, AssetProbeResultDTO> = true;
  const batchMatchesGenerated: TypeEqual<AssetProbeBatchDto, AssetProbeBatchDTO> = true;
  const facadeKeepsOptionalArguments: TypeEqual<
    Parameters<typeof import("../../ipc/commands").assetApi.probeBatch>,
    [assetIds: string[], timeoutMs?: number, maxConcurrent?: number]
  > = true;
  const requestDoesNotReplaceFacade: TypeEqual<
    Parameters<typeof import("../../ipc/commands").assetApi.probeBatch>,
    [request: AssetProbeBatchRequest]
  > = false;
  void [
    resultMatchesGenerated,
    batchMatchesGenerated,
    facadeKeepsOptionalArguments,
    requestDoesNotReplaceFacade,
  ];
}

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

describe("assetReachability overlapping batches", () => {
  it("overlapping a/b batches succeed independently without invalidating each other or getting stuck in checking", async () => {
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

  it("a later batch for the same asset supersedes the earlier batch and discards its late response", async () => {
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

  it("a failed batch clears checking for its own assets and allows probing again", async () => {
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

  it("a failed overlapping batch clears only its own assets and leaves assets taken over by a newer batch unchanged", async () => {
    const p1 = probe(["a", "b"]);
    const p2 = probe(["b"]);
    calls[1].resolve({ results: [result("b", true)] });
    await p2;
    calls[0].reject({ code: "io", message: "batch down" });
    await p1;

    expect(entryOf("b")?.state).toBe("reachable");
    expect(entryOf("a")).toBeUndefined();
  });

  it("uses timeoutMs 2500 and maxConcurrent 8 and deduplicates and truncates IDs to 32", async () => {
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

describe("assetReachability reset isolation", () => {
  it("a late response from before reset never overwrites the post-reset owner's entry for the same asset", async () => {
    const p1 = probe(["shared-asset"]);
    useAssetReachability.getState().reset();
    const p2 = probe(["shared-asset"]);
    expect(mocks.probeBatch).toHaveBeenCalledTimes(2);

    calls[1].resolve({ results: [result("shared-asset", true)] });
    await p2;
    expect(entryOf("shared-asset")?.state).toBe("reachable");

    calls[0].resolve({ results: [result("shared-asset", false, { error: "account-a-late" })] });
    await p1;

    expect(entryOf("shared-asset")?.state).toBe("reachable");
    expect(entryOf("shared-asset")?.error).toBeUndefined();
    expect(useAssetReachability.getState().batchError).toBeNull();
  });

  it("a late rejection from before reset never writes the post-reset owner's batchError", async () => {
    const p1 = probe(["shared-asset"]);
    useAssetReachability.getState().reset();
    const p2 = probe(["shared-asset"]);
    calls[1].resolve({ results: [result("shared-asset", true)] });
    await p2;

    calls[0].reject({ code: "io", message: "account-a-late" });
    await p1;

    expect(entryOf("shared-asset")?.state).toBe("reachable");
    expect(useAssetReachability.getState().batchError).toBeNull();
  });
});

describe("probeAssetReachability in-flight request sharing", () => {
  it("a manual probe waits for an in-flight automatic batch instead of issuing a duplicate request", async () => {
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

  it("a manual probe spanning consecutive batches for the same asset waits for the final batch result", async () => {
    const p1 = probe(["a"]);
    const p2 = probe(["a"]);
    const manual = probeAssetReachability("a");

    calls[1].resolve({ results: [result("a", false, { error: "still down" })] });
    calls[0].resolve({ results: [result("a", true)] });
    await Promise.all([p1, p2]);

    await expect(manual).resolves.toMatchObject({ state: "unreachable" });
    expect(entryOf("a")?.state).toBe("unreachable");
  });

  it("a manual probe starts its own request and returns the result when no request is in flight", async () => {
    const manual = probeAssetReachability("a");
    await Promise.resolve();
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);
    expect(mocks.probeBatch).toHaveBeenCalledWith(["a"], 2500, 8);
    calls[0].resolve({ results: [result("a", true, { durationMs: 31 })] });
    await expect(manual).resolves.toMatchObject({ state: "reachable", durationMs: 31 });
  });

  it("a manual probe returns null instead of a stuck checking state when the in-flight batch fails", async () => {
    const p1 = probe(["a", "b"]);
    const manual = probeAssetReachability("a");
    calls[0].reject({ code: "io", message: "batch down" });
    await p1;
    await expect(manual).resolves.toBeNull();
    expect(entryOf("a")).toBeUndefined();
  });
});
