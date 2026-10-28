/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { EditorView } from "@codemirror/view";
import {
  click,
  clickButton,
  deferred,
  flush,
  mount,
  setInputValue,
  setSelectValue,
  type MountedView,
} from "./reactTestUtils";

async function flushUntil(predicate: () => boolean): Promise<void> {
  await vi.waitFor(async () => {
    await flush();
    expect(predicate()).toBe(true);
  });
}

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  schemas: vi.fn(),
  tables: vi.fn(),
  query: vi.fn(),
  columns: vi.fn(),
  redisScan: vi.fn(),
  redisInspect: vi.fn(),
  disconnect: vi.fn(),
  sessionList: vi.fn(),
  toast: vi.fn(),
  writeText: vi.fn(),
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
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
      disconnect: mocks.disconnect,
    },
    sessionApi: { list: mocks.sessionList },
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
  mocks.ask.mockResolvedValue(true);
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
  mocks.disconnect.mockResolvedValue(undefined);
  mocks.sessionList.mockResolvedValue([]);
  mocks.writeText.mockResolvedValue(undefined);
  Object.defineProperty(window.navigator, "clipboard", {
    value: { writeText: mocks.writeText },
    configurable: true,
  });
  useUi.setState({
    pushToast: mocks.toast,
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    workspaceRuntime: {},
  });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
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

  it("the toolbar exposes an accessible execution history button without a coming-soon placeholder", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    const history = [...mounted!.container.querySelectorAll("button")].find(
      (button) => button.textContent?.trim() === "历史",
    );
    expect(history).not.toBeNull();
    expect(history?.getAttribute("aria-expanded")).toBe("false");
    expect(history?.getAttribute("aria-controls")).toBeTruthy();
    expect(text()).not.toContain("即将推出");
    expect(text()).toContain("草稿仅保留到当前工作区关闭或应用退出");
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
    setSql("SELECT 1;");

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
    expect(editor().state.doc.toString()).toBe("SELECT 1;");
    expect(mocks.ask).not.toHaveBeenCalled();
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

describe("DbPanel SQL drafts", () => {
  function prepareDraftWorkspace(): void {
    useUi.setState({
      workspaces: [
        {
          id: "ws",
          kind: "db",
          title: "database",
          connId: "c1",
          dbKind: "mysql",
          panes: [{ id: "p", tabs: [], activeTabId: null }],
          activePaneId: "p",
          closable: true,
        },
      ],
      activeWorkspaceId: "ws",
      workspaceRuntime: {},
    });
  }

  function mountDraft(connId = "c1"): void {
    mounted = mount(createElement(DbPanel, { connId, kind: "mysql", workspaceId: "ws" }));
  }

  it("restores text, selection, schema, and target after closing and reopening the panel", async () => {
    prepareDraftWorkspace();
    mocks.schemas.mockResolvedValue(["mydb", "other"]);
    mountDraft();
    await flushUntil(() => text().includes("此数据库中没有表"));
    const sql = "SELECT 1;\nSELECT 2;";
    setSql(sql);
    setSelectValue(
      mounted!.container.querySelector<HTMLSelectElement>('select[aria-label="选择数据库"]')!,
      "other",
    );
    act(() => {
      editor().dispatch({ selection: { anchor: 10, head: 19 } });
    });
    await flushUntil(() => {
      const draft = useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft;
      return (
        draft?.text === sql &&
        draft.selection.from === 10 &&
        draft.selection.to === 19 &&
        draft.schema === "other" &&
        draft.connId === "c1"
      );
    });
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft?.dbKind).toBe("mysql");

    mounted!.unmount();
    mounted = undefined;
    mountDraft();
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);

    expect(editor().state.doc.toString()).toBe(sql);
    expect(editor().state.selection.main.from).toBe(10);
    expect(editor().state.selection.main.to).toBe(19);
    await flushUntil(
      () =>
        mounted!.container.querySelector<HTMLSelectElement>('select[aria-label="选择数据库"]')
          ?.value === "other",
    );
    expect(mocks.tables).toHaveBeenLastCalledWith("c1", "other");

    setSql("SELECT 3;");
    await flushUntil(
      () => useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft?.text === "SELECT 3;",
    );
    expect(editor().state.doc.toString()).toBe("SELECT 3;");
  });

  it("keeps the draft and schema while updating the explicit connection target", async () => {
    prepareDraftWorkspace();
    mocks.schemas.mockResolvedValue(["mydb", "other"]);
    mountDraft();
    await flushUntil(() => text().includes("此数据库中没有表"));
    setSql("SELECT * FROM orders;");
    setSelectValue(
      mounted!.container.querySelector<HTMLSelectElement>('select[aria-label="选择数据库"]')!,
      "other",
    );
    await flushUntil(
      () => useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft?.schema === "other",
    );

    act(() => {
      mounted!.root.render(
        createElement(DbPanel, { connId: "c2", kind: "mysql", workspaceId: "ws" }),
      );
    });
    await flushUntil(
      () =>
        mocks.schemas.mock.calls.some((call) => call[0] === "c2") &&
        useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft?.connId === "c2",
    );

    expect(editor().state.doc.toString()).toBe("SELECT * FROM orders;");
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toMatchObject({
      text: "SELECT * FROM orders;",
      schema: "other",
      connId: "c2",
      dbKind: "mysql",
    });
    expect(mocks.tables).toHaveBeenLastCalledWith("c2", "other");
  });

  it("replaces an empty or unmodified generated query without confirmation", async () => {
    mocks.tables.mockResolvedValue(["orders", "users"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("users"));

    clickButton(mounted.container, "orders");
    expect(editor().state.doc.toString()).toBe("SELECT * FROM `mydb`.`orders` LIMIT 200;");
    clickButton(mounted.container, "users");

    expect(editor().state.doc.toString()).toBe("SELECT * FROM `mydb`.`users` LIMIT 200;");
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("preserves a manual draft on cancel and replaces it only after confirmation", async () => {
    mocks.tables.mockResolvedValue(["orders", "users"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("users"));
    const manual = "UPDATE orders SET paid = true;";
    setSql(manual);
    mocks.ask.mockResolvedValueOnce(false);

    clickButton(mounted.container, "users");
    await flushUntil(() => mocks.ask.mock.calls.length === 1);

    expect(editor().state.doc.toString()).toBe(manual);
    expect(mocks.ask.mock.calls[0][0]).toContain("保留草稿并取消");
    expect(mocks.ask.mock.calls[0][0]).toContain("替换查询");
    clickButton(mounted.container, "orders");
    await flushUntil(
      () => editor().state.doc.toString() === "SELECT * FROM `mydb`.`orders` LIMIT 200;",
    );
    expect(mocks.ask).toHaveBeenCalledTimes(2);
  });

  it("rejects a queued draft write after the workspace generation is replaced", async () => {
    prepareDraftWorkspace();
    mountDraft();
    await flushUntil(
      () => useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft?.schema === "mydb",
    );
    expect(useUi.getState().getWorkspaceRuntime("ws")?.generation).toEqual(expect.any(Number));

    setSql("stale write");
    act(() => {
      useUi.setState({ workspaces: [], activeWorkspaceId: null, workspaceRuntime: {} });
    });
    act(() => {
      prepareDraftWorkspace();
    });
    await flush();

    expect(useUi.getState().getWorkspaceRuntime("ws")).toBeUndefined();
  });
});

describe("DbPanel SQL execution history", () => {
  const workspaceId = "ws-history";
  const successfulResult = {
    columns: [],
    rows: [],
    rowsAffected: 1,
    durationMs: 1,
    truncated: false,
    error: null,
  };

  function prepareHistoryWorkspace(): void {
    useUi.setState({
      workspaces: [
        {
          id: workspaceId,
          kind: "db",
          title: "orders database",
          assetId: "asset-db",
          connId: "c1",
          dbKind: "mysql",
          panes: [{ id: "p", tabs: [], activeTabId: null }],
          activePaneId: "p",
          closable: true,
        },
      ],
      activeWorkspaceId: workspaceId,
      workspaceRuntime: {},
      aiBoards: {},
      toasts: [],
    });
  }

  function mountHistory(connId = "c1"): void {
    mounted = mount(createElement(DbPanel, { connId, kind: "mysql", workspaceId }));
  }

  async function ready(): Promise<void> {
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    await flushUntil(() => useUi.getState().getWorkspaceRuntime(workspaceId)?.generation !== undefined);
    await flushUntil(() => useUi.getState().getWorkspaceRuntime(workspaceId)?.sqlDraft?.schema === "mydb");
  }

  function runButton(): HTMLButtonElement {
    const button = [...(mounted?.container.querySelectorAll("button") ?? [])].find((candidate) =>
      candidate.textContent?.includes("运行"),
    );
    if (!button) throw new Error("run button not found");
    return button;
  }

  function records() {
    return useUi.getState().getWorkspaceRuntime(workspaceId)?.sqlExecutionHistory ?? [];
  }

  async function execute(sql: string, count: number): Promise<void> {
    setSql(sql);
    click(runButton());
    await flushUntil(() => records().length === count);
    await flushUntil(() => !runButton().disabled);
  }

  function storeRecord(generation: number, sequence: number) {
    return {
      generation,
      target: {
        connId: `c${sequence}`,
        dbKind: "mysql" as const,
        title: `database ${sequence}`,
        assetId: `asset-${sequence}`,
      },
      sql: `SELECT ${sequence};`,
      schema: "mydb",
      startedAt: sequence * 1000,
      endedAt: sequence * 1000 + 5,
      durationMs: 5,
      rowsAffected: sequence,
      error: null,
      outcome: "success" as const,
    };
  }

  it("keeps only the newest 50 records when the 51st execution arrives", () => {
    prepareHistoryWorkspace();
    useUi.getState().updateSqlDraft(workspaceId, {
      text: "",
      selection: { from: 0, to: 0 },
      schema: "mydb",
      connId: "c1",
      dbKind: "mysql",
    });
    const generation = useUi.getState().getWorkspaceRuntime(workspaceId)!.generation;

    for (let sequence = 1; sequence <= 51; sequence += 1) {
      useUi.getState().recordSqlExecution(workspaceId, storeRecord(generation, sequence));
    }

    expect(records()).toHaveLength(50);
    expect(records()[0].sql).toBe("SELECT 51;");
    expect(records()[49].sql).toBe("SELECT 2;");
    expect(records().some((record) => record.sql === "SELECT 1;")).toBe(false);
    expect(records().every((record) => record.generation === generation)).toBe(true);
  });

  it("bounds the executed SQL and error copies to 32KiB without truncating the request", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    const sql = `SELECT '${"😀".repeat(9000)}';`;
    const query = deferred<never>();
    mocks.query.mockReturnValueOnce(query.promise);

    setSql(sql);
    click(runButton());
    await flushUntil(() => mocks.query.mock.calls.length === 1);
    act(() => {
      query.reject(new Error("e".repeat(33 * 1024)));
    });
    await flush();
    expect(records()).toHaveLength(1);
    expect(runButton().disabled).toBe(false);

    expect(mocks.query).toHaveBeenCalledWith("c1", sql);
    expect(records()[0].sql).not.toBe(sql);
    expect(new TextEncoder().encode(records()[0].sql)).toHaveLength(32 * 1024);
    expect(new TextEncoder().encode(records()[0].error ?? "")).toHaveLength(32 * 1024);
    expect(records()[0].sql.endsWith("😀")).toBe(true);
    expect(records()[0].sql).not.toContain("\uFFFD");
    expect(records()[0].error).not.toContain("\uFFFD");
    expect(records()[0]).toMatchObject({ sqlTruncated: true, errorTruncated: true });

    clickButton(mounted!.container, "历史");
    setSql("");
    const item = mounted!.container.querySelector('ul[aria-label="执行记录"] button');
    expect(item?.textContent).toContain("SQL 已截断");
    expect(item?.textContent).toContain("错误已截断");

    click(item!);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    expect(mocks.ask.mock.calls[0][0]).toContain("不完整副本");
    await flushUntil(() => editor().state.doc.toString() === records()[0].sql);
    expect(editor().state.doc.toString()).not.toBe(sql);
    expect(mocks.query).toHaveBeenCalledTimes(1);
  });

  it("tracks exact 32KiB boundaries and multibyte overflow for SQL and error copies", () => {
    prepareHistoryWorkspace();
    useUi.getState().updateSqlDraft(workspaceId, {
      text: "",
      selection: { from: 0, to: 0 },
      schema: "mydb",
      connId: "c1",
      dbKind: "mysql",
    });
    const generation = useUi.getState().getWorkspaceRuntime(workspaceId)!.generation;
    const exactSql = "😀".repeat(8 * 1024);
    const exactError = "e".repeat(32 * 1024);

    useUi.getState().recordSqlExecution(workspaceId, {
      ...storeRecord(generation, 1),
      sql: exactSql,
      error: exactError,
    });

    expect(records()[0]).toMatchObject({
      sql: exactSql,
      error: exactError,
      sqlTruncated: false,
      errorTruncated: false,
    });
    const crossing = `${"a".repeat(32 * 1024 - 1)}😀`;
    useUi.getState().recordSqlExecution(workspaceId, {
      ...storeRecord(generation, 2),
      sql: crossing,
      error: crossing,
    });

    expect(records()[0]).toMatchObject({
      sql: "a".repeat(32 * 1024 - 1),
      error: "a".repeat(32 * 1024 - 1),
      sqlTruncated: true,
      errorTruncated: true,
    });
    expect(new TextEncoder().encode(records()[0].sql)).toHaveLength(32 * 1024 - 1);
    expect(records()[0].sql).not.toContain("\uFFFD");
    expect(records()[0].error).not.toContain("\uFFFD");
    const overflow = "x".repeat(32 * 1024 + 1);
    useUi.getState().recordSqlExecution(workspaceId, {
      ...storeRecord(generation, 3),
      sql: overflow,
      error: overflow,
    });

    expect(records()[0]).toMatchObject({ sqlTruncated: true, errorTruncated: true });
    expect(new TextEncoder().encode(records()[0].sql)).toHaveLength(32 * 1024);
    expect(new TextEncoder().encode(records()[0].error ?? "")).toHaveLength(32 * 1024);
  });

  it("explains the 50-record limit and workspace-only lifetime in the history panel", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();

    clickButton(mounted!.container, "历史");

    expect(text()).toContain("仅保留最近 50 条");
    expect(text()).toContain("工作区关闭或应用退出后清理");
  });

  it("records selection text and the complete target snapshot captured at submission", async () => {
    prepareHistoryWorkspace();
    mocks.schemas.mockResolvedValue(["mydb", "other"]);
    mountHistory();
    await ready();
    setSelectValue(
      mounted!.container.querySelector<HTMLSelectElement>('select[aria-label="选择数据库"]')!,
      "other",
    );
    await flushUntil(() => mocks.tables.mock.calls.at(-1)?.[1] === "other");
    setSql("SELECT 1;\nSELECT 2;");
    act(() => {
      editor().dispatch({ selection: { anchor: 0, head: 9 } });
    });

    click(runButton());
    await flushUntil(() => records().length === 1 && !runButton().disabled);

    expect(mocks.query).toHaveBeenCalledWith("c1", "SELECT 1;");
    expect(records()[0]).toMatchObject({
      sql: "SELECT 1;",
      schema: "other",
      target: {
        connId: "c1",
        dbKind: "mysql",
        title: "orders database",
        assetId: "asset-db",
      },
      outcome: "success",
    });
  });

  it("records success and SQL errors without caching query result rows", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    mocks.query
      .mockResolvedValueOnce({ ...successfulResult, rowsAffected: 3, durationMs: 7 })
      .mockResolvedValueOnce({ ...successfulResult, rowsAffected: 0, durationMs: 4, error: "bad SQL" });

    await execute("UPDATE orders SET paid = true;", 1);
    expect(records()[0]).toMatchObject({
      outcome: "success",
      rowsAffected: 3,
      durationMs: 7,
      error: null,
    });
    expect(records()[0].startedAt).toEqual(expect.any(Number));
    expect(records()[0].endedAt).toBeGreaterThanOrEqual(records()[0].startedAt);
    expect(records()[0]).not.toHaveProperty("rows");
    expect(records()[0]).not.toHaveProperty("columns");
    expect(records()[0]).not.toHaveProperty("result");

    await execute("SELECT broken;", 2);
    expect(records()[0]).toMatchObject({ outcome: "error", error: "bad SQL", durationMs: 4 });
    clickButton(mounted!.container, "历史");
    const list = mounted!.container.querySelector('ul[aria-label="执行记录"]');
    expect(list?.textContent).toContain("bad SQL");
    expect(list?.textContent).toContain("orders database");
    expect(list?.textContent).toContain("mydb");
    expect(list?.textContent).toContain("4ms");
    expect(list?.textContent).toContain("0 行");
    expect(list?.textContent).toContain(new Date(records()[0].startedAt).toLocaleString());
  });

  it("does not classify plain SQL or untyped error text as an unknown result", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    const errors = [
      'column "timeout" does not exist',
      'relation "network error" does not exist',
      'syntax error at or near "broken pipe"',
      "driver: bad connection",
      'syntax error near "timeout"',
    ];
    for (const [index, error] of errors.entries()) {
      if (index === errors.length - 1) {
        mocks.query.mockRejectedValueOnce(new Error(error));
      } else {
        mocks.query.mockResolvedValueOnce({ ...successfulResult, error });
      }
      await execute(`SELECT ${index};`, index + 1);
      expect(records()[0]).toMatchObject({ outcome: "error", error });
    }

    expect(mocks.query).toHaveBeenCalledTimes(errors.length);
    expect(text()).not.toContain("结果未知");
  });

  it("marks IPC timeout and disconnected errors unknown and never retries writes automatically", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    mocks.query
      .mockRejectedValueOnce({ code: "timeout", message: "SQL 执行超时（1s）" })
      .mockRejectedValueOnce({ code: "disconnected", message: "driver: bad connection" });

    await execute("UPDATE orders SET paid = true;", 1);
    expect(records()[0]).toMatchObject({ outcome: "unknown", rowsAffected: null });
    expect(records()[0].error).toContain("结果未知，请先确认数据库状态");
    expect(text()).toContain("结果未知，请先确认数据库状态");

    await execute("INSERT INTO orders VALUES (1);", 2);
    await flush();
    expect(mocks.query).toHaveBeenCalledTimes(2);
    expect(records()[0]).toMatchObject({ outcome: "unknown", rowsAffected: null });
    expect(records()[0].error).toContain("结果未知，请先确认数据库状态");
    expect(records()[0].error).toContain("driver: bad connection");
  });

  it("marks Web RPC send and response-read disconnections unknown through the real query transport", async () => {
    const actualCommands = await vi.importActual<typeof import("../../ipc/commands")>(
      "../../ipc/commands",
    );
    const { setCsrfToken } = await import("../../ipc/authApi");
    setCsrfToken("csrf-web-rpc");
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce({
        status: 200,
        text: async () => Promise.reject(new TypeError("Response body terminated")),
      });
    vi.stubGlobal("fetch", fetchMock);
    mocks.query.mockImplementation(actualCommands.dbApi.query);
    prepareHistoryWorkspace();
    mountHistory();
    await ready();

    await execute("UPDATE orders SET paid = true;", 1);
    expect(records()[0]).toMatchObject({ outcome: "unknown", rowsAffected: null });
    expect(records()[0].error).toContain("结果未知，请先确认数据库状态");
    expect(records()[0].error).toContain("Failed to fetch");

    await execute("INSERT INTO orders VALUES (1);", 2);
    expect(records()[0]).toMatchObject({ outcome: "unknown", rowsAffected: null });
    expect(records()[0].error).toContain("Response body terminated");
    expect(text()).toContain("结果未知，请先确认数据库状态");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(mocks.query).toHaveBeenCalledTimes(2);
  });

  it("preserves history across panel close and reconnect while keeping each target snapshot", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    await execute("SELECT 1;", 1);
    const first = records()[0];

    mounted!.unmount();
    mounted = undefined;
    mountHistory();
    await ready();
    expect(records()).toEqual([first]);

    act(() => {
      mounted!.root.render(createElement(DbPanel, { connId: "c2", kind: "mysql", workspaceId }));
    });
    await flushUntil(() => mocks.schemas.mock.calls.some((call) => call[0] === "c2"));
    expect(records()).toEqual([first]);
    await execute("SELECT 2;", 2);
    expect(records().map((record) => record.target.connId)).toEqual(["c2", "c1"]);
    expect(records()[1]).toEqual(first);
  });

  it("uses the submission target when an old connection finishes after reconnect", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    const pending = deferred<unknown>();
    mocks.query.mockReturnValueOnce(pending.promise);
    setSql("SELECT old;");
    click(runButton());
    await flushUntil(() => mocks.query.mock.calls.length === 1);

    act(() => {
      mounted!.root.render(createElement(DbPanel, { connId: "c2", kind: "mysql", workspaceId }));
    });
    await flushUntil(() => mocks.schemas.mock.calls.some((call) => call[0] === "c2"));
    pending.resolve(successfulResult);
    await flushUntil(() => records().length === 1);

    expect(records()[0]).toMatchObject({
      sql: "SELECT old;",
      schema: "mydb",
      target: { connId: "c1", dbKind: "mysql", title: "orders database" },
    });
  });

  it("clears history only after the workspace closes successfully", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    await execute("SELECT 1;", 1);
    expect(records()).toHaveLength(1);

    await act(async () => {
      await useUi.getState().closeWorkspace(workspaceId);
    });

    expect(useUi.getState().workspaces.some((workspace) => workspace.id === workspaceId)).toBe(false);
    expect(useUi.getState().getWorkspaceRuntime(workspaceId)).toBeUndefined();
  });

  it("loads a historical UPDATE as a draft with replacement confirmation and never executes it", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    const historical = "UPDATE orders SET paid = true;";
    await execute(historical, 1);
    setSql("SELECT 1;");
    clickButton(mounted!.container, "历史");
    const item = mounted!.container.querySelector('ul[aria-label="执行记录"] button');
    expect(item).not.toBeNull();
    mocks.ask.mockResolvedValueOnce(false);

    click(item!);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    expect(editor().state.doc.toString()).toBe("SELECT 1;");
    expect(mocks.query).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("替换查询");

    click(item!);
    await flushUntil(() => editor().state.doc.toString() === historical);
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    expect(mocks.query).toHaveBeenCalledTimes(1);
  });

  it("rejects a late execution record after the workspace generation is replaced", async () => {
    prepareHistoryWorkspace();
    mountHistory();
    await ready();
    const oldGeneration = useUi.getState().getWorkspaceRuntime(workspaceId)!.generation;
    const pending = deferred<unknown>();
    mocks.query.mockReturnValueOnce(pending.promise);
    setSql("UPDATE orders SET paid = true;");
    click(runButton());
    await flushUntil(() => mocks.query.mock.calls.length === 1);

    act(() => {
      useUi.setState({ workspaces: [], activeWorkspaceId: null, workspaceRuntime: {} });
    });
    act(() => {
      prepareHistoryWorkspace();
      useUi.getState().updateSqlDraft(workspaceId, {
        text: "",
        selection: { from: 0, to: 0 },
        schema: "mydb",
        connId: "c2",
        dbKind: "mysql",
      });
    });
    const newGeneration = useUi.getState().getWorkspaceRuntime(workspaceId)!.generation;
    pending.resolve(successfulResult);
    await flushUntil(() => !runButton().disabled);

    expect(newGeneration).not.toBe(oldGeneration);
    expect(records()).toEqual([]);
  });
});

describe("DbPanel workspace problems", () => {
  const workspaceId = "ws-problems";
  const successfulResult = {
    columns: [],
    rows: [],
    rowsAffected: 1,
    durationMs: 1,
    truncated: false,
    error: null,
  };

  function prepareProblemWorkspace(): void {
    useUi.setState({
      workspaces: [
        {
          id: workspaceId,
          kind: "db",
          title: "orders database",
          assetId: "asset-db",
          connId: "c1",
          dbKind: "mysql",
          panes: [{ id: "p", tabs: [], activeTabId: null }],
          activePaneId: "p",
          closable: true,
        },
      ],
      activeWorkspaceId: workspaceId,
      workspaceRuntime: {},
      aiBoards: {},
      toasts: [],
    });
  }

  function mountProblems(connId = "c1"): void {
    mounted = mount(createElement(DbPanel, { connId, kind: "mysql", workspaceId }));
  }

  async function ready(): Promise<void> {
    await flushUntil(
      () =>
        mounted!.container.querySelector(".cm-content") !== null &&
        useUi.getState().getWorkspaceRuntime(workspaceId)?.generation !== undefined &&
        useUi.getState().getWorkspaceRuntime(workspaceId)?.sqlDraft?.schema === "mydb",
    );
  }

  function runButton(): HTMLButtonElement {
    const button = [...(mounted?.container.querySelectorAll("button") ?? [])].find((candidate) =>
      candidate.textContent?.includes("运行"),
    );
    if (!button) throw new Error("run button not found");
    return button;
  }

  function problems() {
    return useUi.getState().getWorkspaceRuntime(workspaceId)?.problems ?? [];
  }

  async function execute(sql: string, count: number): Promise<void> {
    setSql(sql);
    click(runButton());
    await flushUntil(() => problems().length === count && !runButton().disabled);
  }

  function problemSection(): HTMLElement {
    const section = mounted?.container.querySelector<HTMLElement>('section[aria-label="本次问题"]');
    if (!section) throw new Error("problem section not found");
    return section;
  }

  it("keeps SQL errors in place and copies only redacted problem details", async () => {
    prepareProblemWorkspace();
    mountProblems();
    await ready();
    const detail =
      "bad SQL; password=fixture-password; token=fixture-token; " +
      "postgres://user:dsn-secret@localhost/db; .env ORDINARY=fixture-env";
    mocks.query.mockResolvedValueOnce({ ...successfulResult, error: detail });

    await execute("SELECT broken;", 1);

    expect(problems()[0]).toMatchObject({
      generation: problems()[0].generation,
      source: "sql-query",
      canRetryRead: false,
      sql: "SELECT broken;",
      target: { connId: "c1", title: "orders database", assetId: "asset-db" },
    });
    expect(problems()[0].id).toEqual(expect.any(Number));
    expect(problems()[0].createdAt).toEqual(expect.any(Number));
    expect(problems()[0].detail).not.toContain("fixture-password");
    expect(problems()[0].detail).not.toContain("fixture-token");
    expect(problems()[0].detail).not.toContain("dsn-secret");
    expect(problems()[0].detail).not.toContain("fixture-env");
    const result = mounted!.container.querySelector('div[role="status"]')!;
    const copy = [...result.querySelectorAll("button")].find(
      (button) => button.textContent?.trim() === "复制",
    );
    expect(copy).toBeTruthy();
    click(copy!);
    await flushUntil(() => mocks.writeText.mock.calls.length === 1);
    const copied = mocks.writeText.mock.calls[0][0] as string;
    expect(copied).toContain("bad SQL");
    expect(copied).not.toContain("fixture-password");
    expect(copied).not.toContain("fixture-token");
    expect(copied).not.toContain("dsn-secret");
    expect(copied).not.toContain("fixture-env");

    clickButton(result, "查看问题");
    expect(problemSection().textContent).toContain("orders database");
    expect(problemSection().textContent).toContain("最近 50 条");
  });

  it("keeps transport errors visible and disables recovery when the original connection changes", async () => {
    prepareProblemWorkspace();
    mountProblems();
    await ready();
    mocks.query.mockRejectedValueOnce(new Error("transport failed"));

    await execute("SELECT 1;", 1);
    const result = mounted!.container.querySelector('div[role="status"]')!;
    expect(result.textContent).toContain("transport failed");
    expect(result.textContent).toContain("复制");
    expect(result.textContent).toContain("查看问题");
    clickButton(result, "查看问题");
    expect(
      [...problemSection().querySelectorAll("button")].some(
        (button) => button.textContent?.trim() === "载入 SQL",
      ),
    ).toBe(true);

    act(() => {
      mounted!.root.render(
        createElement(DbPanel, { connId: "c2", kind: "mysql", workspaceId }),
      );
    });
    await flushUntil(() => mocks.schemas.mock.calls.some((call) => call[0] === "c2"));
    expect(problemSection().textContent).toContain("c1");
    expect(
      [...problemSection().querySelectorAll("button")].some(
        (button) => button.textContent?.trim() === "重新发起" || button.textContent?.trim() === "载入 SQL",
      ),
    ).toBe(false);
    expect(mocks.query).toHaveBeenCalledTimes(1);
  });

  it("offers only status guidance and draft reload for an unknown write", async () => {
    prepareProblemWorkspace();
    mountProblems();
    await ready();
    const sql = "UPDATE orders SET paid = true;";
    mocks.query.mockRejectedValueOnce({ code: "disconnected", message: "driver: bad connection" });

    await execute(sql, 1);
    clickButton(mounted!.container, "查看问题");
    expect(problemSection().textContent).toContain("结果未知，请先确认数据库状态");
    expect(problemSection().textContent).not.toContain("重试");
    setSql("SELECT 1;");

    clickButton(problemSection(), "重新发起");
    await flushUntil(() => editor().state.doc.toString() === sql);

    expect(mocks.ask).toHaveBeenCalled();
    expect(mocks.query).toHaveBeenCalledTimes(1);
  });

  it("rejects a deferred SQL problem after reset while preserving draft and execution history", async () => {
    prepareProblemWorkspace();
    mountProblems();
    await ready();
    const pending = deferred<unknown>();
    mocks.query.mockReturnValueOnce(pending.promise);
    setSql("UPDATE orders SET paid = true;");
    click(runButton());
    await flushUntil(() => mocks.query.mock.calls.length === 1);

    act(() => {
      useUi.getState().resetSessions();
    });
    pending.reject(new Error("late SQL failure"));
    await flush();

    expect(problems()).toEqual([]);
    expect(useUi.getState().getWorkspaceRuntime(workspaceId)?.sqlExecutionHistory).toHaveLength(1);
    expect(useUi.getState().getWorkspaceRuntime(workspaceId)?.sqlDraft?.text).toBe(
      "UPDATE orders SET paid = true;",
    );
  });

  it("rejects deferred SQL after account reset and starts the next account with clean runtime", async () => {
    prepareProblemWorkspace();
    mountProblems();
    await ready();
    const oldGeneration = useUi.getState().getWorkspaceRuntime(workspaceId)!.generation;
    const pending = deferred<unknown>();
    mocks.query.mockReturnValueOnce(pending.promise);
    setSql("UPDATE orders SET paid = true;");
    click(runButton());
    await flushUntil(() => mocks.query.mock.calls.length === 1);

    let runtimeAfterReset: ReturnType<typeof useUi.getState>["workspaceRuntime"][string] | undefined;
    act(() => {
      useUi.getState().resetSessions({ clearWorkspaceRuntime: true });
      runtimeAfterReset = useUi.getState().getWorkspaceRuntime(workspaceId);
    });
    const nextGeneration = useUi.getState().getWorkspaceRuntime(workspaceId)!.generation;
    pending.reject(new Error("late SQL failure"));
    await flush();

    expect(runtimeAfterReset).toStrictEqual({ generation: nextGeneration });
    expect(nextGeneration).not.toBe(oldGeneration);
    expect(problems()).toEqual([]);
    expect(useUi.getState().getWorkspaceRuntime(workspaceId)).toStrictEqual({ generation: nextGeneration });
  });

  it("directly retries a failed schema read without re-executing SQL", async () => {
    prepareProblemWorkspace();
    mocks.schemas.mockRejectedValueOnce(new Error("schema down"));
    mountProblems();
    await flushUntil(() => problems().length === 1);
    expect(problems()[0]).toMatchObject({ source: "sql-schemas", canRetryRead: true });

    click(mounted!.container.querySelector('button[aria-label="打开问题列表"]')!);
    clickButton(problemSection(), "重试");

    await flushUntil(() => mocks.schemas.mock.calls.length === 2);
    expect(mocks.query).not.toHaveBeenCalled();
  });
});

describe("DbPanel SQL execution locking", () => {
  const successfulResult = {
    columns: [],
    rows: [],
    rowsAffected: 1,
    durationMs: 1,
    truncated: false,
    error: null,
  };

  function runButton(): HTMLButtonElement {
    const button = [...(mounted?.container.querySelectorAll("button") ?? [])].find((candidate) =>
      candidate.textContent?.includes("运行"),
    );
    if (!button) throw new Error("run button not found");
    return button;
  }

  function pressSql(modifiers: { ctrlKey?: boolean; metaKey?: boolean }): void {
    const host = editor().dom.parentElement;
    if (!host) throw new Error("SQL editor host not found");
    host.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        bubbles: true,
        cancelable: true,
        ...modifiers,
      }),
    );
  }

  function showConnection(connId: string): void {
    act(() => {
      mounted!.root.render(createElement(DbPanel, { connId, kind: "mysql" }));
    });
  }

  it("runs two panels on the same connection independently", async () => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    mocks.query.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    mounted = mount(createElement("div", null,
      createElement("section", { "data-testid": "first-panel" },
        createElement(DbPanel, { connId: "shared", kind: "mysql" })),
      createElement("section", { "data-testid": "second-panel" },
        createElement(DbPanel, { connId: "shared", kind: "mysql" })),
    ));
    const panel = (id: string) => mounted!.container.querySelector<HTMLElement>(`[data-testid="${id}"]`)!;
    const sqlEditor = (id: string) => {
      const element = panel(id).querySelector<HTMLElement>(".cm-editor");
      const view = element ? EditorView.findFromDOM(element) : null;
      if (!view) throw new Error(`SQL editor missing in ${id}`);
      return view;
    };
    const button = (id: string) => {
      const found = [...panel(id).querySelectorAll<HTMLButtonElement>("button")].find(
        (candidate) => candidate.textContent?.includes("运行"),
      );
      if (!found) throw new Error(`Run button missing in ${id}`);
      return found;
    };
    await flushUntil(() => panel("first-panel").querySelector(".cm-editor") !== null &&
      panel("second-panel").querySelector(".cm-editor") !== null);
    act(() => {
      sqlEditor("first-panel").dispatch({ changes: { from: 0, insert: "SELECT 1;" } });
      sqlEditor("second-panel").dispatch({ changes: { from: 0, insert: "SELECT 2;" } });
      button("first-panel").dispatchEvent(new MouseEvent("click", { bubbles: true }));
      button("second-panel").dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(mocks.query.mock.calls).toEqual([
      ["shared", "SELECT 1;"],
      ["shared", "SELECT 2;"],
    ]);
    expect(button("first-panel").disabled).toBe(true);
    expect(button("second-panel").disabled).toBe(true);

    second.resolve({ ...successfulResult, rowsAffected: 2 });
    await flushUntil(() => !button("second-panel").disabled);
    expect(button("first-panel").disabled).toBe(true);
    expect(panel("second-panel").textContent).toContain("2 行受影响");
    expect(panel("first-panel").textContent).not.toContain("2 行受影响");

    first.resolve({ ...successfulResult, rowsAffected: 1 });
    await flushUntil(() => !button("first-panel").disabled);
    expect(panel("first-panel").textContent).toContain("1 行受影响");
    expect(panel("second-panel").textContent).not.toContain("1 行受影响");
  });

  it("claims synchronously across Ctrl, Meta, and button triggers while preserving selection priority", async () => {
    const pending = deferred<unknown>();
    mocks.query.mockReturnValue(pending.promise);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    setSql("SELECT 1;\nSELECT 2;");
    act(() => {
      editor().dispatch({ selection: { anchor: 0, head: 9 } });
    });

    act(() => {
      runButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
      pressSql({ ctrlKey: true });
      pressSql({ metaKey: true });
    });

    expect(mocks.query).toHaveBeenCalledTimes(1);
    expect(mocks.query).toHaveBeenCalledWith("c1", "SELECT 1;");
    pending.resolve(successfulResult);
    await flushUntil(() => !runButton().disabled);
  });

  it("viewing a table structure does not release the in-flight SQL lock", async () => {
    const pending = deferred<unknown>();
    mocks.query.mockReturnValue(pending.promise);
    mocks.tables.mockResolvedValue(["orders"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("orders"));
    setSql("INSERT INTO orders VALUES (1);");
    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(1);

    clickButton(mounted.container, "结构");
    clickButton(mounted.container, "orders");
    await flushUntil(() => mocks.columns.mock.calls.length === 1);
    act(() => {
      pressSql({ ctrlKey: true });
      runButton().dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(mocks.query).toHaveBeenCalledTimes(1);
    expect(runButton().disabled).toBe(true);
    pending.resolve(successfulResult);
    await flushUntil(() => !runButton().disabled);
  });

  it("changing schema does not release the in-flight SQL lock", async () => {
    const pending = deferred<unknown>();
    mocks.query.mockReturnValue(pending.promise);
    mocks.schemas.mockResolvedValue(["mydb", "other"]);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mocks.tables.mock.calls.length === 1);
    setSql("UPDATE orders SET paid = true;");
    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(1);

    setSelectValue(
      mounted.container.querySelector<HTMLSelectElement>('select[aria-label="选择数据库"]')!,
      "other",
    );
    await flushUntil(() => mocks.tables.mock.calls.length === 2);
    act(() => pressSql({ metaKey: true }));

    expect(mocks.query).toHaveBeenCalledTimes(1);
    pending.resolve(successfulResult);
    await flushUntil(() => !runButton().disabled);
  });

  it("allows another explicit run after success, rejection, and a result containing an error", async () => {
    const success = deferred<unknown>();
    const rejection = deferred<unknown>();
    const resultError = deferred<unknown>();
    mocks.query
      .mockReturnValueOnce(success.promise)
      .mockReturnValueOnce(rejection.promise)
      .mockReturnValueOnce(resultError.promise);
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    setSql("SELECT 1;");

    act(() => pressSql({ ctrlKey: true }));
    success.resolve(successfulResult);
    await flushUntil(() => !runButton().disabled);

    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(2);
    rejection.reject(new Error("connection lost"));
    await flushUntil(() => !runButton().disabled);

    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(3);
    resultError.resolve({ ...successfulResult, error: "bad SQL" });
    await flushUntil(() => text().includes("bad SQL") && !runButton().disabled);

    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(4);
    await flushUntil(() => !runButton().disabled);
  });

  it("keeps locks per connection and an old finally cannot release the current connection", async () => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    mocks.query.mockImplementation((connId) => (connId === "a" ? first.promise : second.promise));
    mounted = mount(createElement(DbPanel, { connId: "a", kind: "mysql" }));
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    setSql("SELECT 1;");
    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(1);

    showConnection("b");
    await flush();
    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(2);

    showConnection("a");
    await flush();
    expect(runButton().disabled).toBe(true);
    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(2);

    showConnection("b");
    await flush();
    first.resolve(successfulResult);
    await flush();
    expect(runButton().disabled).toBe(true);
    act(() => pressSql({ metaKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(2);

    second.resolve(successfulResult);
    await flushUntil(() => !runButton().disabled);
  });

  it("an unmounted panel's late finally cannot release a new panel's lock", async () => {
    const oldQuery = deferred<unknown>();
    const currentQuery = deferred<unknown>();
    mocks.query.mockImplementation((connId) =>
      connId === "old" ? oldQuery.promise : currentQuery.promise,
    );
    mounted = mount(createElement(DbPanel, { connId: "old", kind: "mysql" }));
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    setSql("SELECT 1;");
    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(1);

    mounted.unmount();
    mounted = undefined;
    mounted = mount(createElement(DbPanel, { connId: "current", kind: "mysql" }));
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    setSql("SELECT 2;");
    act(() => pressSql({ ctrlKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(2);

    oldQuery.resolve(successfulResult);
    await flush();
    act(() => pressSql({ metaKey: true }));
    expect(mocks.query).toHaveBeenCalledTimes(2);
    expect(runButton().disabled).toBe(true);

    currentQuery.resolve(successfulResult);
    await flushUntil(() => !runButton().disabled);
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
