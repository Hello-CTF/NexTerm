/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
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
    modelOverview: vi.fn(),
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
  modelApi: { overview: mocks.modelOverview },
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
  modelProfileId?: string;
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

type TestProfile = {
  id: string;
  name: string;
  baseUrl: string;
  apiKey: string;
  model: string;
  temperature: number;
  contextWindow: number;
  proxy: string | null;
  stream: boolean;
};

function profile(overrides: Partial<TestProfile> & { id: string; name: string }): TestProfile {
  return {
    baseUrl: "https://api.example.com",
    apiKey: "sk-real-key",
    model: "example-model",
    temperature: 0.3,
    contextWindow: 32768,
    proxy: null,
    stream: true,
    ...overrides,
  };
}

const PROFILE_ACTIVE = profile({ id: "p-active", name: "生产档案" });
const PROFILE_OTHER = profile({ id: "p-other", name: "备用档案", model: "backup-model" });
const PROFILE_MASKED = profile({ id: "p-masked", name: "同步档案", apiKey: "********" });

const OVERVIEW = {
  profiles: [PROFILE_ACTIVE, PROFILE_OTHER, PROFILE_MASKED],
  activeId: "p-active",
};

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
    mocks.modelOverview.mockResolvedValue(OVERVIEW);
    mocks.register.mockResolvedValue(job({ id: "j-new", sessionId: "c-1" }));
    mocks.setEnabled.mockResolvedValue(undefined);
    mocks.unregister.mockResolvedValue(undefined);
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

  it("模型档案选择器：默认项显式指向当前激活档案，列出全部档案且不暴露密钥", async () => {
    mounted = mount(createElement(CronCard));
    await flush();
    clickButton(mounted.container, "注册定时任务");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect(select.value).toBe("");
    const labels = [...select.options].map((o) => o.textContent ?? "");
    expect(labels[0]).toContain("跟随当前激活档案「生产档案」");
    expect(labels.some((l) => l.includes("生产档案 · example-model"))).toBe(true);
    expect(labels.some((l) => l.includes("备用档案 · backup-model"))).toBe(true);
    expect(labels.some((l) => l.includes("同步档案 · example-model（密钥不可用）"))).toBe(true);
    expect(labels.join("\n")).not.toContain("sk-real-key");
    expect(mocks.modelOverview).toHaveBeenCalled();
  });

  it("注册时把所选模型档案随任务持久化", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-9", sessionId: "c-2" }));
    mounted = mount(createElement(CronCard));
    await flush();

    clickButton(mounted.container, "注册定时任务");
    setSelectValue(
      mounted.container.querySelector<HTMLSelectElement>('select[aria-label="模型档案"]')!,
      "p-other",
    );
    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "truncate old logs",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="cron 表达式"]')!,
      "0 4 * * *",
    );
    clickButton(mounted.container, "注册");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: "c-1",
      prompt: "truncate old logs",
      schedule: "0 4 * * *",
      timezone: "UTC",
      modelProfileId: "p-other",
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已注册");
  });

  it("注册允许选择密钥不可用档案，选项中如实标注", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-9", sessionId: "c-1" }));
    mounted = mount(createElement(CronCard));
    await flush();

    clickButton(mounted.container, "注册定时任务");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    setSelectValue(select, "p-masked");
    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "do something",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="cron 表达式"]')!,
      "0 4 * * *",
    );
    clickButton(mounted.container, "注册");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith(
      expect.objectContaining({ modelProfileId: "p-masked" }),
    );
  });

  it("编辑任务：预填当前值并披露重建语义，保存时先建停用替身、删旧后按原状态启用", async () => {
    const withProfile = job({
      id: "j-1",
      sessionId: "c-1",
      name: "磁盘巡检",
      modelProfileId: "p-other",
    });
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [withProfile] : [JOB_B]),
    );
    mocks.register.mockResolvedValue(
      job({ id: "j-10", sessionId: "c-1", modelProfileId: "p-active", enabled: false }),
    );
    mocks.setEnabled.mockResolvedValue(
      job({ id: "j-10", sessionId: "c-1", modelProfileId: "p-active", enabled: true }),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("编辑定时任务");
    expect(text).toContain("任务标识与执行历史不保留");

    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect(select.value).toBe("p-other");
    setSelectValue(select, "p-active");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: "c-1",
      name: "磁盘巡检",
      prompt: "check disk",
      schedule: "0 2 * * *",
      timezone: "UTC",
      timeoutMs: 60_000,
      modelProfileId: "p-active",
      disabled: true,
    });
    expect(mocks.unregister).toHaveBeenCalledWith("c-1", "j-1");
    expect(mocks.setEnabled).toHaveBeenCalledWith("c-1", "j-10", true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("编辑已停用任务时保持停用状态", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-11", sessionId: "c-2", enabled: false }));
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "backup db", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: "c-2",
      prompt: "backup db",
      schedule: "0 3 * * *",
      timezone: "UTC",
      timeoutMs: 60_000,
      disabled: true,
    });
    expect(mocks.unregister).toHaveBeenCalledWith("c-2", "j-2");
    expect(mocks.setEnabled).not.toHaveBeenCalled();
  });

  it("旧任务注销失败（如保存期间开始执行）：回滚清理新任务，不留双任务", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mocks.unregister
      .mockRejectedValueOnce(new Error("任务正在执行，不能注销"))
      .mockResolvedValueOnce(undefined);
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledTimes(1);
    expect(mocks.register).toHaveBeenCalledWith(
      expect.objectContaining({ disabled: true }),
    );
    expect(mocks.unregister).toHaveBeenNthCalledWith(1, "c-1", "j-1");
    expect(mocks.unregister).toHaveBeenNthCalledWith(2, "c-1", "j-10");
    expect(mocks.setEnabled).not.toHaveBeenCalled();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("旧任务注销未成功");
    expect(text).toContain("已清理重建的新任务");
    expect(text).toContain("任务正在执行，不能注销");
    expect(mounted.container.querySelector('textarea[aria-label="任务提示词"]')).not.toBeNull();
    expect(mocks.toast).not.toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("回滚也失败：持久人工处理告警，涉及任务禁止编辑，冲突解决后告警消除", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mocks.unregister.mockRejectedValue(new Error("存储故障"));
    const both = [JOB_A, job({ id: "j-10", sessionId: "c-1", enabled: false })];
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? both : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("请手动注销其中一条");
    expect(text).toContain("c-1/j-1");
    expect(text).toContain("c-1/j-10");
    expect(text).toContain("存储故障");
    const editDisabledFor = (rowText: string) =>
      [...mounted!.container.querySelectorAll("button")].find(
        (b) =>
          b.textContent?.trim() === "编辑" &&
          b.parentElement?.parentElement?.textContent?.includes(rowText),
      )?.disabled;
    expect(editDisabledFor("磁盘巡检")).toBe(true);
    expect(editDisabledFor("check disk")).toBe(true);

    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [JOB_A] : [JOB_B]),
    );
    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).not.toContain("请手动注销其中一条");
  });

  it("回滚失败告警不被刷新前的旧任务列表提前清除，权威刷新确认后才消除", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mocks.unregister.mockRejectedValue(new Error("存储故障"));
    const postConflict = deferred<TestJob[]>();
    mocks.list
      .mockImplementationOnce((sessionId: string) =>
        Promise.resolve(sessionId === "c-1" ? [JOB_A] : [JOB_B]),
      )
      .mockImplementationOnce((sessionId: string) =>
        Promise.resolve(sessionId === "c-1" ? [JOB_A] : [JOB_B]),
      )
      .mockImplementation((sessionId: string) =>
        sessionId === "c-1" ? postConflict.promise : Promise.resolve([JOB_B]),
      );
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mounted.container.textContent).toContain("请手动注销其中一条");
    expect(mounted.container.textContent).toContain("c-1/j-10");

    postConflict.resolve([JOB_A, job({ id: "j-10", sessionId: "c-1", enabled: false })]);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("请手动注销其中一条");
    expect(text).toContain("check disk");
    const editDisabledFor = (rowText: string) =>
      [...mounted!.container.querySelectorAll("button")].find(
        (b) =>
          b.textContent?.trim() === "编辑" &&
          b.parentElement?.parentElement?.textContent?.includes(rowText),
      )?.disabled;
    expect(editDisabledFor("磁盘巡检")).toBe(true);
    expect(editDisabledFor("check disk")).toBe(true);

    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [JOB_A] : [JOB_B]),
    );
    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).not.toContain("请手动注销其中一条");
  });

  it("冲突相关会话读取失败不算已解决，告警保留", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mocks.unregister.mockRejectedValue(new Error("存储故障"));
    const both = [JOB_A, job({ id: "j-10", sessionId: "c-1", enabled: false })];
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? both : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();
    expect(mounted.container.textContent).toContain("请手动注销其中一条");

    mocks.list.mockImplementation((sessionId: string) =>
      sessionId === "c-1"
        ? Promise.reject(new Error("磁盘炸了"))
        : Promise.resolve([JOB_B]),
    );
    clickButton(mounted.container, "刷新");
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("部分会话的任务读取失败");
    expect(text).toContain("请手动注销其中一条");

    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? both : [JOB_B]),
    );
    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).toContain("请手动注销其中一条");

    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [JOB_A] : [JOB_B]),
    );
    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).not.toContain("请手动注销其中一条");
  });

  it("冲突会话从会话列表消失但两条 cron 记录仍在：告警不清除且遗留任务仍可直接查询处理", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mocks.unregister.mockRejectedValue(new Error("存储故障"));
    const both = [JOB_A, job({ id: "j-10", sessionId: "c-1", enabled: false })];
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? both : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();
    expect(mounted.container.textContent).toContain("请手动注销其中一条");

    mocks.conversationList.mockResolvedValue([CONV_B]);
    clickButton(mounted.container, "刷新");
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("请手动注销其中一条");
    expect(text).toContain("c-1/j-1");
    expect(text).toContain("c-1/j-10");
    expect(text).toContain("check disk");

    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [JOB_A] : [JOB_B]),
    );
    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).not.toContain("请手动注销其中一条");
  });

  it("冲突会话消失且直接查询也失败：不算覆盖，告警保留", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mocks.unregister.mockRejectedValue(new Error("存储故障"));
    const both = [JOB_A, job({ id: "j-10", sessionId: "c-1", enabled: false })];
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? both : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();
    expect(mounted.container.textContent).toContain("请手动注销其中一条");

    mocks.conversationList.mockResolvedValue([CONV_B]);
    mocks.list.mockImplementation((sessionId: string) =>
      sessionId === "c-1"
        ? Promise.reject(new Error("存储故障"))
        : Promise.resolve([JOB_B]),
    );
    clickButton(mounted.container, "刷新");
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("部分会话的任务读取失败");
    expect(text).toContain("请手动注销其中一条");
  });

  it("行内启停 pending 时禁止保存；完成后按最新停用状态提交", async () => {
    const slowToggle = deferred<TestJob>();
    mocks.setEnabled.mockReturnValue(slowToggle.promise);
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickRowButton(mounted.container, "磁盘巡检", "停用");
    await flush();

    const save = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存",
    );
    expect(save?.disabled).toBe(true);
    expect(save?.getAttribute("title")).toBe("该行启停保存中，完成后再保存");
    if (save) click(save);
    await flush();
    expect(mocks.register).not.toHaveBeenCalled();

    slowToggle.resolve(job({ id: "j-1", sessionId: "c-1", name: "磁盘巡检", enabled: false }));
    await flush();
    const saveAfter = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存",
    );
    expect(saveAfter?.disabled).toBe(false);
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith(
      expect.objectContaining({ disabled: true }),
    );
    expect(mocks.unregister).toHaveBeenCalledWith("c-1", "j-1");
    expect(mocks.setEnabled).not.toHaveBeenCalledWith("c-1", "j-10", true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("行内启用 pending 时禁止保存；完成后按最新启用状态提交", async () => {
    const slowToggle = deferred<TestJob>();
    mocks.setEnabled.mockReturnValue(slowToggle.promise);
    mocks.register.mockResolvedValue(job({ id: "j-11", sessionId: "c-2", enabled: false }));
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "backup db", "编辑");
    clickRowButton(mounted.container, "backup db", "启用");
    await flush();

    const save = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存",
    );
    expect(save?.disabled).toBe(true);

    slowToggle.resolve(job({ id: "j-2", sessionId: "c-2", enabled: true }));
    await flush();
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith(
      expect.objectContaining({ disabled: true }),
    );
    expect(mocks.unregister).toHaveBeenCalledWith("c-2", "j-2");
    expect(mocks.setEnabled).toHaveBeenCalledWith("c-2", "j-11", true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("替换后启用失败：新任务保持停用并如实提示", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mocks.setEnabled.mockRejectedValue(new Error("存储故障"));
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.setEnabled).toHaveBeenCalledWith("c-1", "j-10", true);
    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("任务已重建但启用失败：存储故障"),
    );
    expect(mounted.container.querySelector('textarea[aria-label="任务提示词"]')).toBeNull();
  });

  it("编辑打开后停用再保存：保存不撤销刚做的停用", async () => {
    mocks.setEnabled.mockResolvedValue(
      job({ id: "j-1", sessionId: "c-1", name: "磁盘巡检", enabled: false }),
    );
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: "c-1", enabled: false }));
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickRowButton(mounted.container, "磁盘巡检", "停用");
    await flush();
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.setEnabled).toHaveBeenCalledWith("c-1", "j-1", false);
    expect(mocks.register).toHaveBeenCalledWith(
      expect.objectContaining({ disabled: true }),
    );
    expect(mocks.unregister).toHaveBeenCalledWith("c-1", "j-1");
    expect(mocks.setEnabled).not.toHaveBeenCalledWith("c-1", "j-10", true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("编辑打开后启用再保存：保存按最新状态启用新任务", async () => {
    mocks.setEnabled
      .mockResolvedValueOnce(job({ id: "j-2", sessionId: "c-2", enabled: true }))
      .mockResolvedValueOnce(job({ id: "j-11", sessionId: "c-2", enabled: true }));
    mocks.register.mockResolvedValue(job({ id: "j-11", sessionId: "c-2", enabled: false }));
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "backup db", "编辑");
    clickRowButton(mounted.container, "backup db", "启用");
    await flush();
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith(
      expect.objectContaining({ disabled: true }),
    );
    expect(mocks.unregister).toHaveBeenCalledWith("c-2", "j-2");
    expect(mocks.setEnabled).toHaveBeenCalledWith("c-2", "j-11", true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("保存进行中锁定该行的启停、注销与编辑", async () => {
    const slow = deferred<TestJob>();
    mocks.register.mockReturnValue(slow.promise);
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    const rowButton = (label: string) =>
      [...mounted!.container.querySelectorAll("button")].find(
        (b) =>
          b.textContent?.trim() === label &&
          b.parentElement?.parentElement?.textContent?.includes("磁盘巡检"),
      );
    expect(rowButton("停用")?.disabled).toBe(true);
    expect(rowButton("注销")?.disabled).toBe(true);
    expect(rowButton("编辑")?.disabled).toBe(true);

    slow.resolve(job({ id: "j-10", sessionId: "c-1" }));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("编辑打开期间该行禁止注销与重复编辑，启停仍可用", async () => {
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    const rowButton = (label: string) =>
      [...mounted!.container.querySelectorAll("button")].find(
        (b) =>
          b.textContent?.trim() === label &&
          b.parentElement?.parentElement?.textContent?.includes("磁盘巡检"),
      );
    expect(rowButton("注销")?.disabled).toBe(true);
    expect(rowButton("注销")?.getAttribute("title")).toBe("编辑中，请先保存或取消编辑");
    expect(rowButton("编辑")?.disabled).toBe(true);
    expect(rowButton("停用")?.disabled).toBe(false);
  });

  it("执行中的任务不能编辑", async () => {
    const running = job({
      id: "j-1",
      sessionId: "c-1",
      name: "磁盘巡检",
      run: { id: "r-1", scheduledFor: "", startedAt: "", deadline: "" },
    });
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [running] : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    const edit = [...mounted.container.querySelectorAll("button")].find(
      (b) =>
        b.textContent?.trim() === "编辑" &&
        b.parentElement?.parentElement?.textContent?.includes("磁盘巡检"),
    );
    expect(edit?.disabled).toBe(true);
    expect(edit?.getAttribute("title")).toBe("执行中不能编辑");
  });

  it("未指定档案的任务显示跟随激活档案", async () => {
    mounted = mount(createElement(CronCard));
    await flush();
    expect(mounted.container.textContent).toContain("跟随激活档案「生产档案」");
  });

  it("任务保存的档案已删除时如实标注，编辑时给出未知档案选项与警告", async () => {
    const orphan = job({
      id: "j-1",
      sessionId: "c-1",
      name: "磁盘巡检",
      modelProfileId: "p-ghost",
    });
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [orphan] : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("档案已删除");
    expect(text).toContain("p-ghost");

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    expect(mounted.container.textContent).toContain("该任务保存的模型档案已不存在");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect(select.value).toBe("p-ghost");
    const labels = [...select.options].map((o) => o.textContent ?? "");
    expect(labels.some((l) => l.includes("未知档案（可能已删除）") && l.includes("p-ghost"))).toBe(
      true,
    );
  });

  it("任务档案密钥不可用时如实标注", async () => {
    const masked = job({
      id: "j-1",
      sessionId: "c-1",
      name: "磁盘巡检",
      modelProfileId: "p-masked",
    });
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [masked] : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("档案密钥不可用");
    expect(text).toContain("档案「同步档案」");
  });

  it("档案读取失败时如实提示，任务列表与注册不受影响", async () => {
    mocks.modelOverview.mockRejectedValue(new Error("档案服务不可用"));
    const withProfile = job({
      id: "j-1",
      sessionId: "c-1",
      name: "磁盘巡检",
      modelProfileId: "p-other",
    });
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [withProfile] : [JOB_B]),
    );
    mocks.register.mockResolvedValue(job({ id: "j-9", sessionId: "c-1" }));
    mounted = mount(createElement(CronCard));
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("磁盘巡检");
    expect(text).toContain("档案 p-other");
    expect(text).not.toContain("档案已删除");

    clickButton(mounted.container, "注册定时任务");
    expect(mounted.container.textContent).toContain("模型档案读取失败：档案服务不可用");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect([...select.options].map((o) => o.textContent?.trim())).toEqual([
      "跟随当前激活档案（读取失败，状态未知）",
    ]);

    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "do something",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="cron 表达式"]')!,
      "0 1 * * *",
    );
    clickButton(mounted.container, "注册");
    await flush();
    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: "c-1",
      prompt: "do something",
      schedule: "0 1 * * *",
      timezone: "UTC",
    });
  });

  it("编辑表单在档案读取失败时显示状态未知，而非误报已删除", async () => {
    mocks.modelOverview.mockRejectedValue(new Error("档案服务不可用"));
    const withProfile = job({
      id: "j-1",
      sessionId: "c-1",
      name: "磁盘巡检",
      modelProfileId: "p-other",
    });
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === "c-1" ? [withProfile] : [JOB_B]),
    );
    mounted = mount(createElement(CronCard));
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("模型档案读取失败：档案服务不可用");
    expect(text).toContain("该任务保存的模型档案状态未知");
    expect(text).not.toContain("可能已删除");
    expect(text).not.toContain("已不存在");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect(select.value).toBe("p-other");
    const labels = [...select.options].map((o) => o.textContent ?? "");
    expect(labels[0]).toContain("跟随当前激活档案（读取失败，状态未知）");
    expect(
      labels.some((l) => l.includes("已存档案（读取失败，状态未知）") && l.includes("p-other")),
    ).toBe(true);
  });

  it("档案加载中时默认项如实显示读取中，加载完成后更新", async () => {
    const slow = deferred<typeof OVERVIEW>();
    mocks.modelOverview.mockReturnValue(slow.promise);
    mounted = mount(createElement(CronCard));
    await flush();

    clickButton(mounted.container, "注册定时任务");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect([...select.options][0]?.textContent).toContain("跟随当前激活档案（读取中…）");

    slow.resolve(OVERVIEW);
    await flush();
    expect([...select.options][0]?.textContent).toContain("跟随当前激活档案「生产档案」");
  });

  it("提交前发现所选档案已删除时拒绝注册并如实提示", async () => {
    mounted = mount(createElement(CronCard));
    await flush();

    clickButton(mounted.container, "注册定时任务");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    setSelectValue(select, "p-other");
    mocks.modelOverview.mockResolvedValue({ profiles: [PROFILE_ACTIVE], activeId: "p-active" });
    act(() => useUi.getState().bumpModelProfilesRevision());
    await flush();

    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "do something",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="cron 表达式"]')!,
      "0 1 * * *",
    );
    clickButton(mounted.container, "注册");
    await flush();

    expect(mocks.register).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("所选模型档案已不存在");
  });

  it("档案变更（revision 递增）后刷新档案数据", async () => {
    mocks.modelOverview.mockResolvedValue({ profiles: [], activeId: null });
    mounted = mount(createElement(CronCard));
    await flush();
    expect(mounted.container.textContent).toContain("跟随激活档案");

    mocks.modelOverview.mockResolvedValue(OVERVIEW);
    act(() => useUi.getState().bumpModelProfilesRevision());
    await flush();
    expect(mounted.container.textContent).toContain("跟随激活档案「生产档案」");
    expect(mocks.modelOverview).toHaveBeenCalledTimes(2);
  });
});
