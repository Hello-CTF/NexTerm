/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
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

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    conversationList: vi.fn(),
    list: vi.fn(),
    register: vi.fn(),
    setEnabled: vi.fn(),
    unregister: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/cron", () => ({
  cronApi: {
    list: mocks.list,
    register: mocks.register,
    setEnabled: mocks.setEnabled,
    unregister: mocks.unregister,
  },
  cronTimeoutMs: (job: { timeout: number }) => Math.round(job.timeout / 1_000_000),
}));
vi.mock("../../ipc/commands", () => ({
  aiApi: { conversationList: mocks.conversationList },
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { CronCard } from "../../features/settings/CronCard";
import { useUi } from "../../app/store";

const CONV_A = { id: "c-1", title: "运维会话" };
const CONV_B = { id: "c-2", title: "数据库会话" };

type TestJob = {
  id: string;
  sessionId: string;
  name?: string;
  prompt: string;
  schedule: string;
  timezone: string;
  enabled: boolean;
  timeout: number;
  createdAt: string;
  updatedAt: string;
  revision: number;
  nextRunAt: string;
  consecutiveFailures?: number;
  lastError?: string;
  lease: { owner?: string };
  run: { id: string; scheduledFor: string; startedAt: string; deadline: string };
};

function job(overrides: Partial<TestJob> & { id: string; sessionId: string }): TestJob {
  return {
    prompt: "check disk",
    schedule: "0 2 * * *",
    timezone: "UTC",
    enabled: true,
    timeout: 60_000_000_000,
    createdAt: "2029-12-31T00:00:00Z",
    updatedAt: "2029-12-31T00:00:00Z",
    revision: 1,
    nextRunAt: "2030-01-01T02:00:00Z",
    lease: {},
    run: { id: "", scheduledFor: "", startedAt: "", deadline: "" },
    ...overrides,
  };
}

const JOB_A = job({ id: "j-1", sessionId: "c-1", name: "磁盘巡检" });
const JOB_B = job({
  id: "j-2",
  sessionId: "c-2",
  prompt: "backup db",
  schedule: "0 3 * * *",
  enabled: false,
  lastError: "permission denied by unattended guard",
  consecutiveFailures: 2,
});

function clickRowButton(container: ParentNode, rowText: string, buttonText: string): void {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) =>
      candidate.textContent?.trim() === buttonText &&
      candidate.parentElement?.parentElement?.textContent?.includes(rowText),
  );
  if (!button) throw new Error(`Row button not found: ${rowText} / ${buttonText}`);
  click(button);
}

describe("CronCard", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: mocks.toast });
    mocks.conversationList.mockResolvedValue([CONV_A, CONV_B]);
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [JOB_A] : [JOB_B]),
    );
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("shows loading first, then jobs aggregated across sessions with status", async () => {
    const slow = deferred<{ id: string; title: string }[]>();
    mocks.conversationList.mockReturnValue(slow.promise);
    mounted = mount(createElement(CronCard));
    expect(mounted.container.textContent).toContain("读取中…");

    slow.resolve([CONV_A, CONV_B]);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("定时任务");
    expect(text).toContain("磁盘巡检");
    expect(text).toContain("backup db");
    expect(text).toContain("运维会话");
    expect(text).toContain("数据库会话");
    expect(text).toContain("等待执行");
    expect(text).toContain("上次失败");
    expect(text).toContain("连续失败 2 次");
    expect(text).toContain("permission denied by unattended guard");
    expect(text).toContain("2 个");
    expect(mocks.list).toHaveBeenCalledWith("c-1");
    expect(mocks.list).toHaveBeenCalledWith("c-2");
  });

  it("shows an empty hint when no conversation has any job", async () => {
    mocks.list.mockResolvedValue([]);
    mounted = mount(createElement(CronCard));
    await flush();
    expect(mounted.container.textContent).toContain("还没有定时任务");
  });

  it("keeps the loaded jobs and names the gap when one session's list fails", async () => {
    mocks.list.mockImplementation((sessionId: string) =>
      sessionId === "c-1" ? Promise.resolve([JOB_A]) : Promise.reject(new Error("磁盘炸了")),
    );
    mounted = mount(createElement(CronCard));
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("部分会话的任务读取失败");
    expect(text).toContain("磁盘炸了");
    expect(text).toContain("磁盘巡检");
    expect(text).not.toContain("backup db");
  });

  it("treats an all-sessions list failure as fatal with a working retry", async () => {
    mocks.list
      .mockRejectedValueOnce(new Error("存储故障"))
      .mockRejectedValueOnce(new Error("存储故障"))
      .mockImplementation((sessionId: string) =>
        Promise.resolve(sessionId === "c-1" ? [JOB_A] : [JOB_B]),
      );
    mounted = mount(createElement(CronCard));
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("读取定时任务失败");
    expect(text).toContain("存储故障");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");
    expect(mocks.list).toHaveBeenCalledTimes(4);
  });

  it("surfaces a conversation-list failure with a working retry", async () => {
    mocks.conversationList
      .mockRejectedValueOnce(new Error("会话服务不可用"))
      .mockResolvedValueOnce([CONV_A]);
    mocks.list.mockResolvedValue([]);
    mounted = mount(createElement(CronCard));
    await flush();
    expect(mounted.container.textContent).toContain("会话服务不可用");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("还没有定时任务");
    expect(mocks.conversationList).toHaveBeenCalledTimes(2);
  });

  it("enables a disabled job in place and reports success", async () => {
    mocks.setEnabled.mockResolvedValue({ ...JOB_B, enabled: true, revision: 2 });
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "backup db", "启用");
    await flush();
    expect(mocks.setEnabled).toHaveBeenCalledWith("c-2", "j-2", true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "任务已启用");
    expect(
      [...mounted.container.querySelectorAll("button")].some(
        (b) =>
          b.textContent?.trim() === "停用" &&
          b.parentElement?.parentElement?.textContent?.includes("backup db"),
      ),
    ).toBe(true);
    expect(mounted.container.textContent).toContain("permission denied by unattended guard");
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("reports honestly and reloads when enable/disable fails", async () => {
    mocks.setEnabled.mockRejectedValue(new Error("存储故障"));
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "停用");
    await flush();
    expect(mocks.setEnabled).toHaveBeenCalledWith("c-1", "j-1", false);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("停用失败"));
    expect(mocks.list).toHaveBeenCalledTimes(4);
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("asks before unregistering and does nothing when the confirmation is declined", async () => {
    mocks.ask.mockResolvedValue(false);
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "注销");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("磁盘巡检");
    expect(question).toContain("运维会话");
    expect(mocks.unregister).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("unregisters only after confirmation, then reloads the list", async () => {
    mocks.ask.mockResolvedValue(true);
    let aJobs: TestJob[] = [JOB_A];
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? aJobs : [JOB_B]),
    );
    mocks.unregister.mockImplementation(() => {
      aJobs = [];
      return Promise.resolve(undefined);
    });
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "注销");
    await flush();
    expect(mocks.unregister).toHaveBeenCalledWith("c-1", "j-1");
    expect(mocks.unregister).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已注销");
    const text = mounted.container.textContent ?? "";
    expect(text).not.toContain("磁盘巡检");
    expect(text).toContain("backup db");
  });

  it("keeps the job and reports when the unregister itself fails", async () => {
    mocks.ask.mockResolvedValue(true);
    mocks.unregister.mockRejectedValue(new Error("只读数据库"));
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "注销");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("注销失败"));
    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("registers a job from the inline form with the mapped arguments", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-9", sessionId: "c-2" }));
    mounted = mount(createElement(CronCard));
    await flush();

    clickButton(mounted.container, "注册定时任务");
    setSelectValue(
      mounted.container.querySelector<HTMLSelectElement>('select[aria-label="所属 AI 会话"]')!,
      "c-2",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="任务名称"]')!,
      "清理日志",
    );
    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "truncate old logs",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="cron 表达式"]')!,
      "0 4 * * *",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>(
        'input[aria-label="单次执行超时（秒）"]',
      )!,
      "120",
    );
    clickButton(mounted.container, "注册");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: "c-2",
      name: "清理日志",
      prompt: "truncate old logs",
      schedule: "0 4 * * *",
      timezone: "UTC",
      timeoutMs: 120_000,
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已注册");
    expect(mocks.list).toHaveBeenCalledTimes(4);
  });

  it("shows the kernel message and keeps the form when registration is rejected", async () => {
    mocks.register.mockRejectedValue({
      code: "bad_param",
      message: "invalid cron schedule",
    });
    mounted = mount(createElement(CronCard));
    await flush();

    clickButton(mounted.container, "注册定时任务");
    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "do something",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="cron 表达式"]')!,
      "not a cron",
    );
    clickButton(mounted.container, "注册");
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("invalid cron schedule");
    expect(mounted.container.querySelector('textarea[aria-label="任务提示词"]')).not.toBeNull();
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("disables registration with a hint when there is no AI conversation yet", async () => {
    mocks.conversationList.mockResolvedValue([]);
    mounted = mount(createElement(CronCard));
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("还没有 AI 会话");
    const button = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.trim() === "注册定时任务",
    );
    expect(button?.disabled).toBe(true);
    expect(mocks.register).not.toHaveBeenCalled();
  });

  it("shows the gap error and retry instead of an empty state when failing sessions hide unknown jobs", async () => {
    mocks.list.mockImplementation((sessionId: string) =>
      sessionId === "c-1" ? Promise.resolve([]) : Promise.reject(new Error("磁盘炸了")),
    );
    mounted = mount(createElement(CronCard));
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("部分会话的任务读取失败");
    expect(text).toContain("磁盘炸了");
    expect(text).toContain("不能据此断定没有任务");
    expect(text).not.toContain("还没有定时任务");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mocks.list).toHaveBeenCalledTimes(4);
  });

  it("shows the refresh failure instead of the stale list when conversationList fails after a successful load", async () => {
    mocks.conversationList
      .mockResolvedValueOnce([CONV_A, CONV_B])
      .mockRejectedValueOnce(new Error("会话服务不可用"));
    mounted = mount(createElement(CronCard));
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");

    clickButton(mounted.container, "刷新");
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("会话服务不可用");
    expect(text).not.toContain("磁盘巡检");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");
    expect(mocks.conversationList).toHaveBeenCalledTimes(3);
  });

  it("drops a stale session-list result that lands after a newer reload", async () => {
    const stale = deferred<TestJob[]>();
    mocks.list
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce([JOB_B])
      .mockResolvedValueOnce([JOB_A])
      .mockResolvedValueOnce([JOB_B]);
    mounted = mount(createElement(CronCard));
    await flush();
    expect(mocks.list).toHaveBeenCalledTimes(2);

    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");

    stale.resolve([]);
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("任务卡片文案：全角冒号、待执行时间点、持久化说明", async () => {
    const noNext = job({ id: "j-3", sessionId: "c-1", nextRunAt: "" });
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [noNext] : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("上次错误：permission denied by unattended guard");
    expect(text).toContain("没有待执行的时间点");
    expect(text).toContain("任务持久保存在本机，重启后继续生效。");
    expect(text).not.toContain("对账");
  });
});
