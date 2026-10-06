/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  click,
  clickButton,
  flushUntil,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  schemas: vi.fn(),
  tables: vi.fn(),
  query: vi.fn(),
  redisScan: vi.fn(),
  redisCommand: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: vi.fn() };
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
      redisInspect: vi.fn(),
      redisCommand: mocks.redisCommand,
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
    columns: ["answer"],
    rows: [["42"]],
    rowsAffected: 1,
    durationMs: 3,
    truncated: false,
    error: null,
  });
  mocks.redisScan.mockResolvedValue([0, []]);
  mocks.redisCommand.mockResolvedValue("OK");
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountRedis(): void {
  mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
}

function cmdInput(): HTMLInputElement {
  const input = mounted?.container.querySelector<HTMLInputElement>('input[aria-label="Redis 命令"]');
  if (!input) throw new Error("命令输入框未找到");
  return input;
}

function statusOut(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>('pre[role="status"]') ?? null;
}

function pressEnter(input: HTMLInputElement): void {
  act(() => {
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  });
}

describe("DbPanel Redis 命令台可访问性", () => {
  it("命令输入有可访问名，输出通过 status 区域播报", async () => {
    mountRedis();
    await flushUntil(() => cmdInput() !== null);
    setInputValue(cmdInput(), "INFO memory");
    clickButton(mounted!.container, "执行");
    await flushUntil(() => statusOut()?.textContent === "OK");
    expect(mocks.redisCommand).toHaveBeenCalledWith("c1", ["INFO", "memory"]);
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("提示文案声明破坏性命令会确认，不再声称没有确认步骤", async () => {
    mountRedis();
    await flushUntil(() => cmdInput() !== null);
    expect(mounted!.container.textContent).toContain("FLUSHALL");
    expect(mounted!.container.textContent).not.toContain("没有确认步骤");
  });
});

describe("DbPanel Redis 命令台危险命令门禁", () => {
  it("FLUSHALL 先弹警告确认；取消则不执行", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    mountRedis();
    await flushUntil(() => cmdInput() !== null);
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

  it("确认后 FLUSHALL 真正执行并播报结果", async () => {
    mountRedis();
    await flushUntil(() => cmdInput() !== null);
    setInputValue(cmdInput(), "FLUSHALL");
    pressEnter(cmdInput());
    await flushUntil(() => statusOut()?.textContent === "OK");
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.redisCommand).toHaveBeenCalledWith("c1", ["FLUSHALL"]);
  });

  it.each(["FLUSHDB", "SHUTDOWN", "flushall"])(
    "%s 同样被门禁拦截，取消即不执行",
    async (command) => {
      mocks.ask.mockResolvedValueOnce(false);
      mountRedis();
      await flushUntil(() => cmdInput() !== null);
      setInputValue(cmdInput(), command);
      pressEnter(cmdInput());
      await flushUntil(() => mocks.ask.mock.calls.length > 0);
      expect(mocks.ask).toHaveBeenCalledWith(
        expect.stringContaining(command.toUpperCase()),
        expect.objectContaining({ kind: "warning" }),
      );
      expect(mocks.redisCommand).not.toHaveBeenCalled();
    },
  );
});

describe("DbPanel MySQL 可访问性", () => {
  it("SQL 编辑器有可访问名，查询结果在 status 区域播报", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    expect(mounted!.container.querySelector(".cm-content")?.getAttribute("aria-label")).toBe(
      "SQL 编辑器",
    );
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
});
