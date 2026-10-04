/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, clickButton, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  schemas: vi.fn(),
  tables: vi.fn(),
  columns: vi.fn(),
  redisScan: vi.fn(),
  redisInspect: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dbApi: {
      schemas: mocks.schemas,
      tables: mocks.tables,
      query: vi.fn(),
      columns: mocks.columns,
      redisScan: mocks.redisScan,
      redisInspect: mocks.redisInspect,
      redisCommand: vi.fn(),
      redisSetTtl: vi.fn(),
    },
    sessionApi: {},
    terminalApi: {},
    vaultApi: {},
  };
});

import { DbPanel } from "../../features/db/DbPanel";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.schemas.mockResolvedValue(["mydb"]);
  mocks.tables.mockResolvedValue([]);
  mocks.columns.mockResolvedValue({ columns: [], indexes: [] });
  mocks.redisScan.mockResolvedValue([0, []]);
  mocks.redisInspect.mockResolvedValue({ key: "k1", keyType: "string", ttl: -1, value: "v" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function text(): string {
  return mounted?.container.textContent ?? "";
}

describe("DbPanel MySQL 状态", () => {
  it("schema 列表失败时给出真实错误与重试，而不是「没有表」", async () => {
    mocks.schemas.mockRejectedValue(new Error("连接已断开"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("数据库列表加载失败 · 连接已断开"));
    expect(text()).not.toContain("这个库里没有表");
  });

  it("重试后恢复，真空库才显示「这个库里没有表」", async () => {
    mocks.schemas.mockRejectedValueOnce(new Error("连接已断开"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("数据库列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("这个库里没有表"));
    expect(mocks.schemas).toHaveBeenCalledTimes(2);
  });

  it("表列表失败时内联报错并可重试", async () => {
    mocks.tables.mockRejectedValueOnce(new Error("权限不足"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("表列表加载失败 · 权限不足"));
    expect(text()).not.toContain("这个库里没有表");
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("这个库里没有表"));
  });

  it("表列表成功后渲染表名 chips", async () => {
    mocks.tables.mockResolvedValue(["orders", "users"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("users"));
    expect(text()).not.toContain("表列表加载中");
  });
});

describe("DbPanel Redis 状态", () => {
  it("SCAN 失败时给出真实错误与重试，而不是「没有匹配的键」", async () => {
    mocks.redisScan.mockRejectedValue(new Error("LOADING 错误"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("键列表加载失败 · LOADING 错误"));
    expect(text()).not.toContain("没有匹配的键");
  });

  it("重试后恢复，空结果才显示「没有匹配的键」", async () => {
    mocks.redisScan.mockRejectedValueOnce(new Error("LOADING 错误"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("键列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("没有匹配的键"));
    expect(mocks.redisScan).toHaveBeenCalledTimes(2);
  });

  it("键详情失败时内联报错并可重试", async () => {
    mocks.redisScan.mockResolvedValue([0, ["k1"]]);
    mocks.redisInspect.mockRejectedValue(new Error("WRONGTYPE"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("k1"));
    const row = [...mounted!.container.querySelectorAll(".nx-row")].find((r) =>
      r.textContent?.includes("k1"),
    );
    expect(row).toBeTruthy();
    click(row!);
    await flushUntil(() => text().includes("键内容加载失败 · WRONGTYPE"));
    expect(text()).not.toContain("从左侧选一个键查看内容");
    mocks.redisInspect.mockResolvedValue({ key: "k1", keyType: "string", ttl: -1, value: "v" });
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("k1"));
  });
});
