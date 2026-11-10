/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { EditorView } from "@codemirror/view";
import {
  click,
  clickButton,
  flush,
  flushUntil as flushUntilRaw,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

async function flushUntil(predicate: () => boolean): Promise<void> {
  if (predicate()) {
    await flush();
    return;
  }
  await flushUntilRaw(predicate);
}

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  schemas: vi.fn(),
  tables: vi.fn(),
  query: vi.fn(),
  redisScan: vi.fn(),
  redisInspect: vi.fn(),
  redisCommand: vi.fn(),
  redisSetTtl: vi.fn(),
  toast: vi.fn(),
  writeText: vi.fn(),
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dbApi: {
      schemas: mocks.schemas,
      tables: mocks.tables,
      query: mocks.query,
      columns: vi.fn(),
      redisScan: mocks.redisScan,
      redisInspect: mocks.redisInspect,
      redisCommand: mocks.redisCommand,
      redisSetTtl: mocks.redisSetTtl,
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
    columns: ["answer"],
    rows: [["42"]],
    rowsAffected: 1,
    durationMs: 3,
    truncated: false,
    error: null,
  });
  mocks.redisScan.mockResolvedValue([0, []]);
  mocks.redisInspect.mockResolvedValue({ key: "k1", keyType: "string", ttl: -1, value: "v" });
  mocks.redisCommand.mockResolvedValue("OK");
  mocks.redisSetTtl.mockResolvedValue(undefined);
  mocks.promptText.mockResolvedValue(null);
  mocks.ask.mockResolvedValue(true);
  mocks.writeText.mockResolvedValue(undefined);
  Object.defineProperty(window.navigator, "clipboard", {
    value: { writeText: mocks.writeText },
    configurable: true,
  });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountRedis(): void {
  mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
}

function mountMysql(): void {
  mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
}

function setSql(sql: string): void {
  const element = mounted?.container.querySelector<HTMLElement>(".cm-editor");
  const view = element ? EditorView.findFromDOM(element) : null;
  if (!view) throw new Error("SQL editor not found");
  act(() => {
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: sql } });
  });
}

function cmdInput(): HTMLInputElement {
  const input = mounted?.container.querySelector<HTMLInputElement>('input[aria-label="Redis 命令"]');
  if (!input) throw new Error("command input not found");
  return input;
}

async function expandConsole(): Promise<void> {
  const bar = [...(mounted?.container.querySelectorAll("button") ?? [])].find((b) =>
    b.textContent?.includes("控制台"),
  );
  if (!bar) throw new Error("console collapse bar not found");
  click(bar);
  await flushUntil(
    () => mounted?.container.querySelector('input[aria-label="Redis 命令"]') !== null,
  );
}

function statusOut(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>('pre[role="status"]') ?? null;
}

function pressEnter(input: HTMLInputElement): void {
  act(() => {
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  });
}

async function selectKey(key: string): Promise<void> {
  mocks.redisScan.mockResolvedValue([0, [key]]);
  mountRedis();
  await flushUntil(() => mounted!.container.textContent?.includes(key) === true);
  const row = [...mounted!.container.querySelectorAll(".nx-row")].find((r) =>
    r.textContent?.includes(key),
  );
  if (!row) throw new Error(`key row not found: ${key}`);
  click(row);
  await flushUntil(() =>
    [...mounted!.container.querySelectorAll("button")].some(
      (b) => b.textContent?.trim() === "修改过期时间",
    ),
  );
}

describe("DbPanel Redis console accessibility", () => {
  it("the command input has an accessible name and output is announced via a status region", async () => {
    mountRedis();
    await expandConsole();
    setInputValue(cmdInput(), "INFO memory");
    clickButton(mounted!.container, "执行");
    await flushUntil(() => statusOut()?.textContent === "OK");
    expect(mocks.redisCommand).toHaveBeenCalledWith("c1", ["INFO", "memory"]);
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("the hint explains the dangerous-command confirmation rule, quoting and history", async () => {
    mountRedis();
    await expandConsole();
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("使用 Redis 命令协议，不支持 SQL");
    expect(text).toContain("危险命令执行前需要确认");
    expect(text).toContain("其余命令直接执行");
    expect(text).toContain("参数可用引号包裹");
    expect(text).not.toContain("不支持引号");
    expect(text).not.toContain("没有确认步骤");
  });
});

function pressKey(input: HTMLInputElement, key: string, init: KeyboardEventInit = {}): void {
  act(() => {
    input.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
  });
}

describe("DbPanel Redis console arguments and history", () => {
  it("sends quoted arguments as single redis-cli style arguments", async () => {
    mountRedis();
    await expandConsole();
    setInputValue(cmdInput(), `SET greeting "hello world\\n" 'it\\'s'`);
    pressEnter(cmdInput());
    await flushUntil(() => mocks.redisCommand.mock.calls.length === 1);
    expect(mocks.redisCommand).toHaveBeenCalledWith("c1", ["SET", "greeting", "hello world\n", "it's"]);
  });

  it("shows unbalanced quotes as an error without sending the command", async () => {
    mountRedis();
    await expandConsole();
    setInputValue(cmdInput(), 'SET k "open');
    pressEnter(cmdInput());
    await flushUntil(() => statusOut()?.textContent?.includes("引号不匹配") === true);
    expect(mocks.redisCommand).not.toHaveBeenCalled();
  });

  it("walks command history with the arrow keys and ignores them during composition", async () => {
    mountRedis();
    await expandConsole();
    for (const command of ["GET a", "GET b"]) {
      setInputValue(cmdInput(), command);
      pressEnter(cmdInput());
      await flushUntil(() => mocks.redisCommand.mock.calls.at(-1)?.[1]?.join(" ") === command);
    }
    setInputValue(cmdInput(), "GET dr");
    pressKey(cmdInput(), "ArrowUp", { isComposing: true });
    expect(cmdInput().value).toBe("GET dr");
    pressKey(cmdInput(), "ArrowUp");
    expect(cmdInput().value).toBe("GET b");
    pressKey(cmdInput(), "ArrowUp");
    expect(cmdInput().value).toBe("GET a");
    pressKey(cmdInput(), "ArrowDown");
    expect(cmdInput().value).toBe("GET b");
    pressKey(cmdInput(), "ArrowDown");
    expect(cmdInput().value).toBe("GET dr");
  });
});

describe("DbPanel Redis console collapse", () => {
  it("collapsed to a one-line bar by default; expandable, and collapsing returns to the bar keeping an output preview", async () => {
    mountRedis();
    await flushUntil(() => mounted!.container.textContent?.includes("控制台") === true);
    expect(mounted!.container.querySelector('input[aria-label="Redis 命令"]')).toBeNull();
    const bar = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("控制台"),
    );
    expect(bar?.getAttribute("aria-expanded")).toBe("false");

    await expandConsole();
    setInputValue(cmdInput(), "INFO memory");
    pressEnter(cmdInput());
    await flushUntil(() => statusOut()?.textContent === "OK");

    clickButton(mounted!.container, "收起");
    await flushUntil(
      () => mounted!.container.querySelector('input[aria-label="Redis 命令"]') === null,
    );
    const collapsedBar = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("控制台"),
    );
    expect(collapsedBar?.textContent).toContain("OK");
  });

  it("the collapse bar and collapse button are linked to the console region via aria-controls", async () => {
    mountRedis();
    await flushUntil(() => mounted!.container.textContent?.includes("控制台") === true);
    const bar = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("控制台"),
    );
    if (!bar) throw new Error("console collapse bar not found");
    const panelId = bar.getAttribute("aria-controls");
    expect(panelId).toBeTruthy();
    expect(bar.getAttribute("aria-expanded")).toBe("false");

    await expandConsole();
    const panel = mounted!.container.querySelector(".nx-redis-console");
    expect(panel?.id).toBe(panelId);
    const collapse = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "收起",
    );
    expect(collapse?.getAttribute("aria-expanded")).toBe("true");
    expect(collapse?.getAttribute("aria-controls")).toBe(panelId);

    clickButton(mounted!.container, "收起");
    await flushUntil(() => mounted!.container.querySelector(".nx-redis-console") === null);
    const collapsedBar = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("控制台"),
    );
    expect(collapsedBar?.getAttribute("aria-expanded")).toBe("false");
    expect(collapsedBar?.getAttribute("aria-controls")).toBe(panelId);
  });
});

describe("DbPanel Redis console dangerous-command gate", () => {
  it("FLUSHALL asks for warning confirmation first; cancel does not execute", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    mountRedis();
    await expandConsole();
    setInputValue(cmdInput(), "FLUSHALL");
    clickButton(mounted!.container, "执行");
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("FLUSHALL"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.redisCommand).not.toHaveBeenCalled();
    expect(statusOut()).toBeNull();
  });

  it("confirmed FLUSHALL actually executes and announces the result", async () => {
    mountRedis();
    await expandConsole();
    setInputValue(cmdInput(), "FLUSHALL");
    pressEnter(cmdInput());
    await flushUntil(() => statusOut()?.textContent === "OK");
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.redisCommand).toHaveBeenCalledWith("c1", ["FLUSHALL"]);
  });

  it.each([
    "FLUSHDB",
    "SHUTDOWN",
    "DEL",
    "UNLINK",
    "CONFIG",
    "DEBUG",
    "EVAL",
    "EVALSHA",
    "FCALL",
    "flushall",
  ])("%s is also gated; cancel does not execute", async (command) => {
    mocks.ask.mockResolvedValueOnce(false);
    mountRedis();
    await expandConsole();
    setInputValue(cmdInput(), command);
    pressEnter(cmdInput());
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining(command.toUpperCase()),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.redisCommand).not.toHaveBeenCalled();
  });
});

describe("DbPanel Redis edit TTL", () => {
  it("non-integer input errors immediately without calling the backend", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("abc");
    clickButton(mounted!.container, "修改过期时间");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.redisSetTtl).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("整数"));
  });

  it("decimal input is also rejected", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("1.5");
    clickButton(mounted!.container, "修改过期时间");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.redisSetTtl).not.toHaveBeenCalled();
  });

  it.each(["-2", "-42"])("negatives other than -1 such as %s are rejected without calling the backend", async (input) => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce(input);
    clickButton(mounted!.container, "修改过期时间");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.redisSetTtl).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("整数"));
  });

  it("-1 is submitted as usual (never expire)", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("-1");
    clickButton(mounted!.container, "修改过期时间");
    await flushUntil(() => mocks.redisSetTtl.mock.calls.length > 0);
    expect(mocks.redisSetTtl).toHaveBeenCalledWith("c1", "k1", -1);
    await flushUntil(() => mocks.toast.mock.calls.some((c) => c[0] === "success"));
    expect(mocks.toast).toHaveBeenCalledWith("success", "过期时间已更新");
  });

  it("entering 0 warns that the key is deleted immediately, then submits after confirmation", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("0");
    clickButton(mounted!.container, "修改过期时间");
    await flushUntil(() => mocks.redisSetTtl.mock.calls.length > 0);
    expect(mocks.promptText).toHaveBeenCalledWith(
      expect.stringContaining("填 0 会立即删除该键"),
      expect.anything(),
    );
    expect(mocks.redisSetTtl).toHaveBeenCalledWith("c1", "k1", 0);
    await flushUntil(() => mocks.toast.mock.calls.some((c) => c[0] === "success"));
    expect(mocks.toast).toHaveBeenCalledWith("success", "过期时间已更新");
  });

  it("backend failure reports the error, with no success toast and no refresh", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("120");
    mocks.redisSetTtl.mockRejectedValueOnce(new Error("NOAUTH Authentication required."));
    clickButton(mounted!.container, "修改过期时间");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "NOAUTH Authentication required.");
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.anything());
    expect(mocks.redisInspect).toHaveBeenCalledTimes(1);
  });
});

describe("DbPanel MySQL accessibility", () => {
  it("the SQL editor has an accessible name and query results are announced via a status region", async () => {
    mountMysql();
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    expect(mounted!.container.querySelector(".cm-content")?.getAttribute("aria-label")).toBe(
      "SQL 编辑器",
    );
    setSql("SELECT 42;");
    const run = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("运行"),
    );
    expect(run).toBeTruthy();
    click(run!);
    await flushUntil(() =>
      mounted!.container.querySelector('div[role="status"]')?.textContent?.includes("42") === true,
    );
    expect(mocks.query).toHaveBeenCalledTimes(1);
  });

  it("the run button and empty state state that the statement under the cursor runs", async () => {
    mountMysql();
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    const run = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("运行"),
    );
    expect(run?.title).toContain("运行光标所在的语句");
    expect(run?.title).toContain("一次只执行一条语句");
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("+Enter 运行光标所在的语句");
    expect(text).not.toContain("运行整段");
  });
});

describe("DbPanel SQL statement under the cursor", () => {
  function editorView(): EditorView {
    return EditorView.findFromDOM(mounted!.container.querySelector<HTMLElement>(".cm-editor")!)!;
  }

  function runButton(): HTMLButtonElement {
    const run = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent?.includes("运行"));
    if (!run) throw new Error("run button not found");
    return run;
  }

  it("runs only the statement under the cursor when nothing is selected", async () => {
    mountMysql();
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    setSql("SELECT 1;\nSELECT 'a;b';\nSELECT 3;");
    act(() => {
      editorView().dispatch({ selection: { anchor: 13 } });
    });
    click(runButton());
    await flushUntil(() => mocks.query.mock.calls.length === 1);
    expect(mocks.query).toHaveBeenCalledWith("c1", "SELECT 'a;b';");
  });

  it("tells the user when there is no statement to run", async () => {
    mountMysql();
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    setSql("-- only a comment\n");
    click(runButton());
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("info", "没有可运行的 SQL 语句");
    expect(mocks.query).not.toHaveBeenCalled();
  });
});

describe("DbPanel MySQL copy-CSV feedback", () => {
  async function runQuery(): Promise<void> {
    mountMysql();
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    setSql("SELECT 42;");
    const run = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("运行"),
    );
    if (!run) throw new Error("run button not found");
    click(run);
    await flushUntil(() =>
      [...mounted!.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "复制 CSV",
      ),
    );
  }

  it("a success toast is shown after copying", async () => {
    await runQuery();
    clickButton(mounted!.container, "复制 CSV");
    await flushUntil(() => mocks.writeText.mock.calls.length > 0);
    expect(mocks.writeText).toHaveBeenCalledWith('answer\n"42"');
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "CSV 已复制");
  });

  it("a failure toast is shown instead of silence", async () => {
    await runQuery();
    mocks.writeText.mockRejectedValueOnce(new Error("clipboard unavailable"));
    clickButton(mounted!.container, "复制 CSV");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "复制失败：clipboard unavailable");
  });
});
