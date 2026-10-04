/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";
import {
  orderQuickConnectAssets,
  quickConnectScore,
  useConnectHistory,
  type ConnectHistoryEntries,
} from "../../features/explorer/connectHistory";

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

describe("quickConnectScore 最近/频次权重", () => {
  it("无记录时得分为 0", () => {
    expect(quickConnectScore(undefined, NOW)).toBe(0);
  });

  it("越新得分越高", () => {
    const older = { count: 1, at: NOW - 6 * 3_600_000 };
    const newer = { count: 1, at: NOW - 1_000 };
    expect(quickConnectScore(newer, NOW)).toBeGreaterThan(quickConnectScore(older, NOW));
  });

  it("同等新近下频次越高得分越高", () => {
    const once = { count: 1, at: NOW - 1_000 };
    const often = { count: 8, at: NOW - 1_000 };
    expect(quickConnectScore(often, NOW)).toBeGreaterThan(quickConnectScore(once, NOW));
  });

  it("未来的时间戳不会产生超指数分数", () => {
    const future = { count: 1, at: NOW + 60_000 };
    expect(quickConnectScore(future, NOW)).toBeLessThanOrEqual(1.1);
  });
});

describe("orderQuickConnectAssets 排序与过滤", () => {
  const assets = [
    asset("a-web", "web-01", { host: "10.0.0.8", username: "root" }),
    asset("a-db", "db-01", { host: "10.0.0.9", username: "dba", tags: "生产, mysql" }),
    asset("a-build", "build-01", { host: "10.0.0.40", username: "ci" }),
    asset("a-local", "当前设备"),
  ];

  it("空查询时按最近使用排序，未连接的按名称", () => {
    const entries: ConnectHistoryEntries = {
      "a-build": { count: 2, at: NOW - 2 * 24 * 3_600_000 },
      "a-db": { count: 1, at: NOW - 10 * 60_000 },
    };
    const ordered = orderQuickConnectAssets(assets, entries, "", NOW);
    expect(ordered.map((a) => a.id)).toEqual(["a-db", "a-build", "a-web", "a-local"]);
  });

  it("名称前缀匹配优先于名称包含，再优先于主机/用户/标签", () => {
    const entries: ConnectHistoryEntries = {};
    const list = [
      asset("a1", "web-01"),
      asset("a2", "my-web-01"),
      asset("a3", "backend", { host: "web-01.internal" }),
    ];
    const ordered = orderQuickConnectAssets(list, entries, "web", NOW);
    expect(ordered.map((a) => a.id)).toEqual(["a1", "a2", "a3"]);
  });

  it("匹配内部按最近使用排序", () => {
    const entries: ConnectHistoryEntries = {
      "a2": { count: 1, at: NOW - 60_000 },
      "a1": { count: 1, at: NOW - 5 * 60_000 },
    };
    const list = [asset("a1", "web-01"), asset("a2", "web-02")];
    const ordered = orderQuickConnectAssets(list, entries, "web", NOW);
    expect(ordered.map((a) => a.id)).toEqual(["a2", "a1"]);
  });

  it("主机、用户名与标签均可命中", () => {
    const entries: ConnectHistoryEntries = {};
    expect(orderQuickConnectAssets(assets, entries, "10.0.0.9", NOW).map((a) => a.id)).toEqual([
      "a-db",
    ]);
    expect(orderQuickConnectAssets(assets, entries, "dba", NOW).map((a) => a.id)).toEqual(["a-db"]);
    expect(orderQuickConnectAssets(assets, entries, "生产", NOW).map((a) => a.id)).toEqual(["a-db"]);
  });

  it("大小写不敏感且忽略首尾空白", () => {
    const entries: ConnectHistoryEntries = {};
    expect(orderQuickConnectAssets(assets, entries, "  WEB-01 ", NOW).map((a) => a.id)).toEqual([
      "a-web",
    ]);
  });

  it("无匹配时返回空数组", () => {
    expect(orderQuickConnectAssets(assets, {}, "不存在的东西", NOW)).toEqual([]);
  });
});

describe("useConnectHistory 记录", () => {
  beforeEach(() => {
    localStorage.clear();
    useConnectHistory.setState({ entries: {} });
  });

  it("record 累加次数并刷新时间", () => {
    useConnectHistory.getState().record("a1");
    const first = useConnectHistory.getState().entries["a1"];
    expect(first?.count).toBe(1);
    useConnectHistory.getState().record("a1");
    const second = useConnectHistory.getState().entries["a1"];
    expect(second?.count).toBe(2);
    expect(second?.at).toBeGreaterThanOrEqual(first?.at ?? 0);
  });

  it("写入 localStorage 以便跨会话保留", () => {
    useConnectHistory.getState().record("a1");
    const raw = localStorage.getItem("nexterm.connectHistory.v1");
    expect(raw).toBeTruthy();
    expect(JSON.parse(raw ?? "{}")).toHaveProperty("a1");
  });
});
