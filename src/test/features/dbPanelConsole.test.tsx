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

async function selectKey(key: string): Promise<void> {
  mocks.redisScan.mockResolvedValue([0, [key]]);
  mountRedis();
  await flushUntil(() => mounted!.container.textContent?.includes(key) === true);
  const row = [...mounted!.container.querySelectorAll(".nx-row")].find((r) =>
    r.textContent?.includes(key),
  );
  if (!row) throw new Error(`键行未找到: ${key}`);
  click(row);
  await flushUntil(() =>
    [...mounted!.container.querySelectorAll("button")].some(
      (b) => b.textContent?.trim() === "改过期时间",
    ),
  );
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

  it("提示文案如实枚举需确认的命令，并声明引号限制", async () => {
    mountRedis();
    await flushUntil(() => cmdInput() !== null);
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("FLUSHALL");
    expect(text).toContain("DEL");
    expect(text).toContain("EVAL");
    expect(text).toContain("执行前会要求确认");
    expect(text).toContain("其余命令立即执行");
    expect(text).toContain("不支持引号");
    expect(text).not.toContain("没有确认步骤");
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
  ])("%s 同样被门禁拦截，取消即不执行", async (command) => {
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
  });
});

describe("DbPanel Redis 改过期时间", () => {
  it("非整数输入直接报错，不调用后端", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("abc");
    clickButton(mounted!.container, "改过期时间");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.redisSetTtl).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("整数"));
  });

  it("小数输入同样拒绝", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("1.5");
    clickButton(mounted!.container, "改过期时间");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.redisSetTtl).not.toHaveBeenCalled();
  });

  it("输入 0 时提示会立即删除该键，确认后照常提交", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("0");
    clickButton(mounted!.container, "改过期时间");
    await flushUntil(() => mocks.redisSetTtl.mock.calls.length > 0);
    expect(mocks.promptText).toHaveBeenCalledWith(
      expect.stringContaining("填 0 会立即删除该键"),
      expect.anything(),
    );
    expect(mocks.redisSetTtl).toHaveBeenCalledWith("c1", "k1", 0);
    await flushUntil(() => mocks.toast.mock.calls.some((c) => c[0] === "success"));
    expect(mocks.toast).toHaveBeenCalledWith("success", "过期时间已更新");
  });

  it("后端失败时报错，不再提示成功、不再刷新", async () => {
    await selectKey("k1");
    mocks.promptText.mockResolvedValueOnce("120");
    mocks.redisSetTtl.mockRejectedValueOnce(new Error("NOAUTH 未授权"));
    clickButton(mounted!.container, "改过期时间");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "NOAUTH 未授权");
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.anything());
    expect(mocks.redisInspect).toHaveBeenCalledTimes(1);
  });
});

describe("DbPanel MySQL 可访问性", () => {
  it("SQL 编辑器有可访问名，查询结果在 status 区域播报", async () => {
    mountMysql();
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

  it("运行按钮与空态如实声明单语句限制与立即生效边界", async () => {
    mountMysql();
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    const run = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("运行"),
    );
    expect(run?.title).toContain("一次只执行一条语句");
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("直接在远端库执行并立即生效");
    expect(text).toContain("一次只执行一条语句");
  });
});

describe("DbPanel MySQL 复制 CSV 反馈", () => {
  async function runQuery(): Promise<void> {
    mountMysql();
    await flushUntil(() => mounted!.container.querySelector(".cm-content") !== null);
    const run = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("运行"),
    );
    if (!run) throw new Error("运行按钮未找到");
    click(run);
    await flushUntil(() =>
      [...mounted!.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "复制 CSV",
      ),
    );
  }

  it("复制成功后给出成功提示", async () => {
    await runQuery();
    clickButton(mounted!.container, "复制 CSV");
    await flushUntil(() => mocks.writeText.mock.calls.length > 0);
    expect(mocks.writeText).toHaveBeenCalledWith('answer\n"42"');
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "CSV 已复制");
  });

  it("复制失败时给出错误提示而不是静默", async () => {
    await runQuery();
    mocks.writeText.mockRejectedValueOnce(new Error("剪贴板不可用"));
    clickButton(mounted!.container, "复制 CSV");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "复制失败：剪贴板不可用");
  });
});
