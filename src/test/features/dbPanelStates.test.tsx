/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { EditorView } from "@codemirror/view";
import {
  click,
  clickButton,
  deferred,
  flush,
  flushUntil,
  mount,
  setInputValue,
  setSelectValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  schemas: vi.fn(),
  tables: vi.fn(),
  query: vi.fn(),
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
      query: mocks.query,
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
  mocks.query.mockResolvedValue({
    columns: [],
    rows: [],
    rowsAffected: 0,
    durationMs: 1,
    truncated: false,
    error: null,
  });
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

function editor(): EditorView {
  const element = mounted?.container.querySelector<HTMLElement>(".cm-editor");
  const view = element ? EditorView.findFromDOM(element) : null;
  if (!view) throw new Error("SQL editor not found");
  return view;
}

function setSql(sql: string): void {
  const view = editor();
  act(() => {
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: sql } });
  });
}

function keyRow(key: string): Element {
  const row = [...(mounted?.container.querySelectorAll(".nx-row") ?? [])].find(
    (candidate) => candidate.textContent?.trim() === key,
  );
  if (!row) throw new Error(`key row not found: ${key}`);
  return row;
}

describe("DbPanel MySQL states", () => {
  it("schema list failure shows the real error with a retry, not the no-tables state", async () => {
    mocks.schemas.mockRejectedValue(new Error("connection lost"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("数据库列表加载失败 · connection lost"));
    expect(text()).not.toContain("此数据库中没有表");
  });

  it("after retry it recovers; only a truly empty database shows the no-tables state", async () => {
    mocks.schemas.mockRejectedValueOnce(new Error("connection lost"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("数据库列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("此数据库中没有表"));
    expect(mocks.schemas).toHaveBeenCalledTimes(2);
  });

  it("table list failure shows an inline error and can be retried", async () => {
    mocks.tables.mockRejectedValueOnce(new Error("permission denied"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("表列表加载失败 · permission denied"));
    expect(text()).not.toContain("此数据库中没有表");
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("此数据库中没有表"));
  });

  it("table names render as chips after a successful load", async () => {
    mocks.tables.mockResolvedValue(["orders", "users"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("users"));
    expect(text()).not.toContain("表列表加载中");
  });

  it("the toolbar has no dead History (coming soon) button", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    expect(
      [...mounted!.container.querySelectorAll("button")].some((b) =>
        b.textContent?.includes("历史"),
      ),
    ).toBe(false);
    expect(mounted!.container.querySelector('button[title="查询历史（即将推出）"]')).toBeNull();
  });
});

describe("DbPanel PostgreSQL states", () => {
  it("selects the first non-system schema by default and labels the title PostgreSQL", async () => {
    mocks.schemas.mockResolvedValue(["pg_catalog", "public"]);
    mocks.tables.mockResolvedValue(["orders"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "postgres" }));
    await flushUntil(() => text().includes("orders"));
    expect(text()).toContain("PostgreSQL");
    expect(mocks.tables).toHaveBeenCalledWith("c1", "public");
  });

  it("an empty schema shows the no-tables-or-views state", async () => {
    mocks.schemas.mockResolvedValue(["public"]);
    mocks.tables.mockResolvedValue([]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "postgres" }));
    await flushUntil(() => text().includes("此 schema 中没有表或视图"));
  });
});

describe("DbPanel Redis states", () => {
  it("SCAN failure shows the real error with a retry, not the no-keys state", async () => {
    mocks.redisScan.mockRejectedValue(new Error("LOADING error"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("键列表加载失败 · LOADING error"));
    expect(text()).not.toContain("没有匹配的键");
  });

  it("after retry it recovers; only an empty result shows no matching keys", async () => {
    mocks.redisScan.mockRejectedValueOnce(new Error("LOADING error"));
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("键列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("没有匹配的键"));
    expect(mocks.redisScan).toHaveBeenCalledTimes(2);
  });

  it("key detail failure shows an inline error and can be retried", async () => {
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
    await flushUntil(
      () =>
        !text().includes("键内容加载失败") &&
        (mounted!.container.querySelector(".nx-pre")?.textContent ?? "").includes('"v"'),
    );
  });

  it("key detail loading shows pending instead of the pick-a-key placeholder", async () => {
    mocks.redisScan.mockResolvedValue([0, ["k1"]]);
    let resolveInspect!: (v: unknown) => void;
    mocks.redisInspect.mockImplementation(
      () => new Promise((resolve) => (resolveInspect = resolve)),
    );
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("k1"));
    const row = [...mounted!.container.querySelectorAll(".nx-row")].find((r) =>
      r.textContent?.includes("k1"),
    );
    expect(row).toBeTruthy();
    click(row!);
    await flushUntil(() => text().includes("键内容加载中…"));
    expect(text()).not.toContain("从左侧选一个键查看内容");
    resolveInspect({ key: "k1", keyType: "string", ttl: -1, value: "v" });
    await flushUntil(() =>
      (mounted!.container.querySelector(".nx-pre")?.textContent ?? "").includes('"v"'),
    );
  });

  it("an empty page with a non-zero cursor prompts for the next page instead of no matching keys", async () => {
    mocks.redisScan.mockResolvedValueOnce([5, []]).mockResolvedValueOnce([0, ["k1"]]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("本页没有匹配的键"));
    const exactEmpty = [...mounted!.container.querySelectorAll(".nx-hint")].some(
      (d) => d.textContent?.trim() === "没有匹配的键",
    );
    expect(exactEmpty).toBe(false);
    clickButton(mounted!.container, "下一页");
    await flushUntil(() => text().includes("k1"));
    expect(text()).not.toContain("本页没有匹配");
  });

  it("a fresh SCAN clears the pagination error after a failed next page", async () => {
    mocks.redisScan
      .mockResolvedValueOnce([5, ["k1"]])
      .mockRejectedValueOnce(new Error("connection reset"))
      .mockResolvedValueOnce([0, ["k2"]]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("k1"));
    clickButton(mounted!.container, "下一页");
    await flushUntil(() => text().includes("下一页加载失败 · connection reset"));
    clickButton(mounted!.container, "SCAN");
    await flushUntil(() => !text().includes("下一页加载失败") && text().includes("k2"));
    expect(text()).toContain("k2");
  });
});

describe("DbPanel SQL regressions", () => {
  it("MySQL chips use the current database and escape the full identifier", async () => {
    mocks.schemas.mockResolvedValue(["mydb", "Sales DB"]);
    mocks.tables.mockImplementation((_connId, schema) =>
      Promise.resolve(schema === "Sales DB" ? ["Order `select"] : ["orders"]),
    );
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("orders"));
    setSelectValue(
      mounted.container.querySelector<HTMLSelectElement>('select[aria-label="选择数据库"]')!,
      "Sales DB",
    );
    await flushUntil(() => text().includes("Order `select"));

    clickButton(mounted.container, "Order `select");

    expect(editor().state.doc.toString()).toBe(
      "SELECT * FROM `Sales DB`.`Order ``select` LIMIT 200;",
    );
  });

  it("PostgreSQL chips use double quotes and double embedded quotes", async () => {
    mocks.schemas.mockResolvedValue(["public"]);
    mocks.tables.mockResolvedValue(['Order "Details']);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "postgres" }));
    await flushUntil(() => text().includes('Order "Details'));

    clickButton(mounted.container, 'Order "Details');

    expect(editor().state.doc.toString()).toBe(
      'SELECT * FROM "public"."Order ""Details" LIMIT 200;',
    );
  });

  it("a stale schema's table-list response does not override the new selection", async () => {
    const first = deferred<string[]>();
    const second = deferred<string[]>();
    mocks.schemas.mockResolvedValue(["first", "second"]);
    mocks.tables.mockImplementation((_connId, schema) =>
      schema === "first" ? first.promise : second.promise,
    );
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mocks.tables.mock.calls.length === 1);

    setSelectValue(
      mounted.container.querySelector<HTMLSelectElement>('select[aria-label="选择数据库"]')!,
      "second",
    );
    await flushUntil(() => mocks.tables.mock.calls.length === 2);
    second.resolve(["new_table"]);
    await flushUntil(() => text().includes("new_table"));
    first.resolve(["old_table"]);
    await flush();

    expect(text()).toContain("new_table");
    expect(text()).not.toContain("old_table");
  });

  it("structure shows fields and indexes separately without a fake duration", async () => {
    mocks.tables.mockResolvedValue(["Order Details"]);
    mocks.columns.mockResolvedValue({
      columns: [
        {
          name: "id",
          type: "BIGINT",
          nullable: false,
          key: "PRI",
          default: null,
          extra: "auto_increment",
        },
      ],
      indexes: [{ name: "idx_order", unique: false, column: "id", seq: 1 }],
    });
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("Order Details"));

    clickButton(mounted.container, "结构");
    clickButton(mounted.container, "Order Details");
    await flushUntil(
      () => mounted!.container.querySelector('table[aria-label="索引列表"]') !== null,
    );

    const fields = mounted.container.querySelector('table[aria-label="字段列表"]');
    const indexes = mounted.container.querySelector('table[aria-label="索引列表"]');
    expect(fields?.textContent).toContain("id");
    expect(fields?.textContent).not.toContain("idx_order");
    expect(indexes?.textContent).toContain("idx_order");
    expect(indexes?.textContent).toContain("INDEX");
    expect(text()).not.toContain("8ms");
  });

  it("the table list expands all and searches all tables while collapsed", async () => {
    mocks.tables.mockResolvedValue(
      Array.from({ length: 65 }, (_, index) => `table_${String(index).padStart(2, "0")}`),
    );
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("table_00"));
    expect(text()).not.toContain("table_64");

    clickButton(mounted.container, "展开全部");
    await flushUntil(() => text().includes("table_64"));
    clickButton(mounted.container, "收起");
    await flushUntil(() => !text().includes("table_64"));

    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="搜索全部表"]')!,
      "table_64",
    );
    await flushUntil(() => text().includes("table_64"));
    expect(text()).not.toContain("table_63");
  });

  it("manual refresh refetches the current database's tables", async () => {
    mocks.tables
      .mockResolvedValueOnce(["orders"])
      .mockResolvedValueOnce(["orders", "users"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("orders"));

    clickButton(mounted.container, "刷新");

    await flushUntil(() => text().includes("users"));
    expect(mocks.tables).toHaveBeenCalledTimes(2);
    expect(mocks.tables).toHaveBeenLastCalledWith("c1", "mydb");
  });

  it("a successful DDL auto-refreshes and shows the new table", async () => {
    mocks.tables
      .mockResolvedValueOnce(["orders"])
      .mockResolvedValueOnce(["orders", "audit log"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("orders"));
    const sql = "CREATE TABLE `audit log` (id INT);";
    setSql(sql);
    const run = [...mounted.container.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("运行"),
    );
    expect(run).toBeTruthy();

    click(run!);
    await flushUntil(() =>
      [...mounted!.container.querySelectorAll("button")].some(
        (button) => button.textContent?.trim() === "audit log",
      ),
    );

    expect(mocks.query).toHaveBeenCalledWith("c1", sql);
    expect(mocks.tables).toHaveBeenCalledTimes(2);
  });
});

describe("DbPanel Redis race regressions", () => {
  it("a stale key-detail response does not override the newly selected key", async () => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    mocks.redisScan.mockResolvedValue([0, ["k1", "k2"]]);
    mocks.redisInspect.mockImplementation((_connId, key) =>
      key === "k1" ? first.promise : second.promise,
    );
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("k2"));

    click(keyRow("k1"));
    await flushUntil(() => mocks.redisInspect.mock.calls.length === 1);
    click(keyRow("k2"));
    await flushUntil(() => mocks.redisInspect.mock.calls.length === 2);
    second.resolve({ key: "k2", keyType: "string", ttl: -1, value: "new" });
    await flushUntil(() => mounted!.container.querySelector(".nx-pre")?.textContent === '"new"');
    first.resolve({ key: "k1", keyType: "string", ttl: -1, value: "old" });
    await flush();

    expect(mounted.container.querySelector(".nx-pre")?.textContent).toBe('"new"');
    expect(text()).not.toContain('"old"');
  });

  it("duplicate requests are ignored while the same SCAN is in flight", async () => {
    const pending = deferred<[number, string[]]>();
    mocks.redisScan.mockReturnValue(pending.promise);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => mocks.redisScan.mock.calls.length === 1);

    clickButton(mounted.container, "SCAN");
    await flush();

    expect(mocks.redisScan).toHaveBeenCalledTimes(1);
    pending.resolve([0, []]);
    await flushUntil(() => text().includes("没有匹配的键"));
  });

  it("keys are deduplicated across the first and later SCAN pages", async () => {
    mocks.redisScan
      .mockResolvedValueOnce([7, ["k1", "k1", "k2"]])
      .mockResolvedValueOnce([0, ["k2", "k3"]]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("k2"));

    clickButton(mounted.container, "下一页");
    await flushUntil(() => text().includes("k3"));

    expect(
      [...mounted.container.querySelectorAll(".nx-row")].map((row) => row.textContent?.trim()),
    ).toEqual(["k1", "k2", "k3"]);
  });

  it("a late response for an old pattern does not override the new scan", async () => {
    const initial = deferred<[number, string[]]>();
    const current = deferred<[number, string[]]>();
    mocks.redisScan.mockImplementation((_connId, _cursor, scanPattern) =>
      scanPattern === "new:*" ? current.promise : initial.promise,
    );
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => mocks.redisScan.mock.calls.length === 1);

    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="键匹配模式"]')!,
      "new:*",
    );
    clickButton(mounted.container, "SCAN");
    await flushUntil(() => mocks.redisScan.mock.calls.length === 2);
    current.resolve([0, ["new-key"]]);
    await flushUntil(() => text().includes("new-key"));
    initial.resolve([0, ["old-key"]]);
    await flush();

    expect(text()).toContain("new-key");
    expect(text()).not.toContain("old-key");
  });

  it("a late page response for an old cursor does not override the fresh scan", async () => {
    const page = deferred<[number, string[]]>();
    const refresh = deferred<[number, string[]]>();
    mocks.redisScan
      .mockResolvedValueOnce([5, ["root"]])
      .mockImplementationOnce(() => page.promise)
      .mockImplementationOnce(() => refresh.promise);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("root"));

    clickButton(mounted.container, "下一页");
    await flushUntil(() => mocks.redisScan.mock.calls.length === 2);
    clickButton(mounted.container, "SCAN");
    await flushUntil(() => mocks.redisScan.mock.calls.length === 3);
    refresh.resolve([0, ["fresh"]]);
    await flushUntil(() => text().includes("fresh"));
    page.resolve([0, ["stale"]]);
    await flush();

    expect(text()).toContain("fresh");
    expect(text()).not.toContain("stale");
    expect(
      [...mounted.container.querySelectorAll("button")].some(
        (button) => button.textContent?.trim() === "下一页",
      ),
    ).toBe(false);
  });
});
