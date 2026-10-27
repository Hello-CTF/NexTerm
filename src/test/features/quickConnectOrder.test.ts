/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";
import {
  quickConnectScore,
  useConnectHistory,
  type ConnectHistoryEntries,
} from "../../features/explorer/connectHistory";
import { hostCandidates, rankJumpItems } from "../../app/jump/jumpRanking";

interface TestAsset {
  id: string;
  name: string;
  host: string | null;
  username: string | null;
  tags: string;
}

function asset(id: string, name: string, extra: Partial<TestAsset> = {}): TestAsset {
  return { id, name, host: null, username: null, tags: "", ...extra };
}

const NOW = 1_700_000_000_000;

function orderQuickConnectAssets(list: TestAsset[], entries: ConnectHistoryEntries, query: string, now: number): TestAsset[] {
  return rankJumpItems(hostCandidates(list, (item) => quickConnectScore(entries[item.id], now)), query).map((item) => item.asset);
}

describe("quickConnectScore recency and frequency weighting", () => {
  it("returns zero without history", () => {
    expect(quickConnectScore(undefined, NOW)).toBe(0);
  });

  it("scores newer connections higher", () => {
    const older = { count: 1, at: NOW - 6 * 3_600_000 };
    const newer = { count: 1, at: NOW - 1_000 };
    expect(quickConnectScore(newer, NOW)).toBeGreaterThan(quickConnectScore(older, NOW));
  });

  it("scores more frequent connections higher at the same recency", () => {
    const once = { count: 1, at: NOW - 1_000 };
    const often = { count: 8, at: NOW - 1_000 };
    expect(quickConnectScore(often, NOW)).toBeGreaterThan(quickConnectScore(once, NOW));
  });

  it("caps scores for future timestamps", () => {
    const future = { count: 1, at: NOW + 60_000 };
    expect(quickConnectScore(future, NOW)).toBeLessThanOrEqual(1.1);
  });
});

describe("jump palette host ranking and filtering", () => {
  const assets = [
    asset("a-web", "web-01", { host: "10.0.0.8", username: "root" }),
    asset("a-db", "db-01", { host: "10.0.0.9", username: "dba", tags: "production, mysql" }),
    asset("a-build", "build-01", { host: "10.0.0.40", username: "ci" }),
    asset("a-local", "zz-current device"),
  ];

  it("sorts by recency for an empty query and by name for unconnected assets", () => {
    const entries: ConnectHistoryEntries = {
      "a-build": { count: 2, at: NOW - 2 * 24 * 3_600_000 },
      "a-db": { count: 1, at: NOW - 10 * 60_000 },
    };
    const ordered = orderQuickConnectAssets(assets, entries, "", NOW);
    expect(ordered.map((a) => a.id)).toEqual(["a-db", "a-build", "a-web", "a-local"]);
  });

  it("ranks name-prefix matches ahead of name substring matches, then host, username, and tags", () => {
    const entries: ConnectHistoryEntries = {};
    const list = [
      asset("a1", "web-01"),
      asset("a2", "my-web-01"),
      asset("a3", "backend", { host: "web-01.internal" }),
    ];
    const ordered = orderQuickConnectAssets(list, entries, "web", NOW);
    expect(ordered.map((a) => a.id)).toEqual(["a1", "a2", "a3"]);
  });

  it("sorts matches by recency", () => {
    const entries: ConnectHistoryEntries = {
      "a2": { count: 1, at: NOW - 60_000 },
      "a1": { count: 1, at: NOW - 5 * 60_000 },
    };
    const list = [asset("a1", "web-01"), asset("a2", "web-02")];
    const ordered = orderQuickConnectAssets(list, entries, "web", NOW);
    expect(ordered.map((a) => a.id)).toEqual(["a2", "a1"]);
  });

  it("matches hosts, usernames, and tags", () => {
    const entries: ConnectHistoryEntries = {};
    expect(orderQuickConnectAssets(assets, entries, "10.0.0.9", NOW).map((a) => a.id)).toEqual([
      "a-db",
    ]);
    expect(orderQuickConnectAssets(assets, entries, "dba", NOW).map((a) => a.id)).toEqual(["a-db"]);
    expect(orderQuickConnectAssets(assets, entries, "production", NOW).map((a) => a.id)).toEqual(["a-db"]);
  });

  it("matches case-insensitively and trims surrounding whitespace", () => {
    const entries: ConnectHistoryEntries = {};
    expect(orderQuickConnectAssets(assets, entries, "  WEB-01 ", NOW).map((a) => a.id)).toEqual([
      "a-web",
    ]);
  });

  it("returns an empty array when nothing matches", () => {
    expect(orderQuickConnectAssets(assets, {}, "missing asset", NOW)).toEqual([]);
  });
});

describe("useConnectHistory history", () => {
  beforeEach(() => {
    localStorage.clear();
    useConnectHistory.setState({ entries: {} });
  });

  it("record increments the count and refreshes the timestamp", () => {
    useConnectHistory.getState().record("a1");
    const first = useConnectHistory.getState().entries["a1"];
    expect(first?.count).toBe(1);
    useConnectHistory.getState().record("a1");
    const second = useConnectHistory.getState().entries["a1"];
    expect(second?.count).toBe(2);
    expect(second?.at).toBeGreaterThanOrEqual(first?.at ?? 0);
  });

  it("persists records to localStorage across sessions", () => {
    useConnectHistory.getState().record("a1");
    const raw = localStorage.getItem("nexterm.connectHistory.v1");
    expect(raw).toBeTruthy();
    expect(JSON.parse(raw ?? "{}")).toHaveProperty("a1");
  });
});
