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
    list: vi.fn(),
    register: vi.fn(),
    setEnabled: vi.fn(),
    unregister: vi.fn(),
    modelOverview: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
    onClose: vi.fn(),
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
  modelApi: { overview: mocks.modelOverview },
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { CronPanel } from "../../features/ai/CronPanel";
import { useUi } from "../../app/store";

const CONV_ID = "c-1";

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

const JOB_A = job({ id: "j-1", sessionId: CONV_ID, name: "磁盘巡检" });

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

function mountPanel(conversationId = CONV_ID): MountedView {
  return mount(createElement(CronPanel, { conversationId, onClose: mocks.onClose }));
}

describe("CronPanel", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: mocks.toast });
    mocks.list.mockResolvedValue([JOB_A]);
    mocks.modelOverview.mockResolvedValue(OVERVIEW);
    mocks.register.mockResolvedValue(job({ id: "j-new", sessionId: CONV_ID }));
    mocks.setEnabled.mockResolvedValue(undefined);
    mocks.unregister.mockResolvedValue(undefined);
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("只读取并展示当前会话的定时任务", async () => {
    mounted = mountPanel();
    expect(mounted.container.textContent).toContain("读取中…");
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("定时任务");
    expect(text).toContain("磁盘巡检");
    expect(text).toContain("等待执行");
    expect(text).toContain("1 个");
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.list).toHaveBeenCalledWith(CONV_ID);
  });

  it("切换会话后按新会话重新读取", async () => {
    const other = job({ id: "j-2", sessionId: "c-2", prompt: "backup db" });
    mocks.list.mockImplementation((sessionId: string) =>
      Promise.resolve(sessionId === CONV_ID ? [JOB_A] : [other]),
    );
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");

    act(() => {
      mounted!.root.render(
        createElement(CronPanel, { conversationId: "c-2", onClose: mocks.onClose }),
      );
    });
    await flush();
    expect(mocks.list).toHaveBeenLastCalledWith("c-2");
    expect(mounted.container.textContent).toContain("backup db");
    expect(mounted.container.textContent).not.toContain("磁盘巡检");
  });

  it("空列表显示当前会话的空提示", async () => {
    mocks.list.mockResolvedValue([]);
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain("这个会话还没有定时任务");
  });

  it("读取失败展示错误并可重试", async () => {
    mocks.list.mockRejectedValueOnce(new Error("存储故障")).mockResolvedValueOnce([JOB_A]);
    mounted = mountPanel();
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("读取定时任务失败");
    expect(text).toContain("存储故障");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("刷新失败时保留错误而不是旧列表，重试后恢复", async () => {
    mocks.list.mockResolvedValueOnce([JOB_A]).mockRejectedValueOnce(new Error("存储故障"));
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");

    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("存储故障");
    expect(text).not.toContain("磁盘巡检");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("丢弃晚到的旧列表结果", async () => {
    const stale = deferred<TestJob[]>();
    mocks.list
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce([JOB_A]);
    mounted = mountPanel();
    await flush();

    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");

    stale.resolve([]);
    await flush();
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("停用当前会话的任务并原位更新", async () => {
    mocks.setEnabled.mockResolvedValue({ ...JOB_A, enabled: false, revision: 2 });
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "停用");
    await flush();
    expect(mocks.setEnabled).toHaveBeenCalledWith(CONV_ID, "j-1", false);
    expect(mocks.toast).toHaveBeenCalledWith("success", "任务已停用");
    expect(mounted.container.textContent).toContain("已停用");
    expect(mocks.list).toHaveBeenCalledTimes(1);
  });

  it("启停失败时如实提示并重新读取", async () => {
    mocks.setEnabled.mockRejectedValue(new Error("存储故障"));
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "停用");
    await flush();
    expect(mocks.setEnabled).toHaveBeenCalledWith(CONV_ID, "j-1", false);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("停用失败"));
    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("注销前先确认，拒绝则不动作", async () => {
    mocks.ask.mockResolvedValue(false);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "注销");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("磁盘巡检");
    expect(question).not.toContain("所属会话");
    expect(mocks.unregister).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("确认注销后重新读取，任务消失", async () => {
    mocks.ask.mockResolvedValue(true);
    let jobs: TestJob[] = [JOB_A];
    mocks.list.mockImplementation(() => Promise.resolve(jobs));
    mocks.unregister.mockImplementation(() => {
      jobs = [];
      return Promise.resolve(undefined);
    });
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "注销");
    await flush();
    expect(mocks.unregister).toHaveBeenCalledWith(CONV_ID, "j-1");
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已注销");
    expect(mounted.container.textContent).not.toContain("磁盘巡检");
    expect(mounted.container.textContent).toContain("这个会话还没有定时任务");
  });

  it("注销失败时保留任务并提示", async () => {
    mocks.ask.mockResolvedValue(true);
    mocks.unregister.mockRejectedValue(new Error("只读数据库"));
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "注销");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("注销失败"));
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("注册表单不再提供所属会话选择，自动落在当前会话", async () => {
    mounted = mountPanel();
    await flush();

    clickButton(mounted.container, "注册定时任务");
    expect(mounted.container.querySelector('select[aria-label="所属 AI 会话"]')).toBeNull();
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
    clickButton(mounted.container, "高级设置");
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>(
        'input[aria-label="单次执行超时（秒）"]',
      )!,
      "120",
    );
    clickButton(mounted.container, "注册");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: CONV_ID,
      name: "清理日志",
      prompt: "truncate old logs",
      schedule: "0 4 * * *",
      timezone: "UTC",
      timeoutMs: 120_000,
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已注册");
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("注册表单默认折叠高级设置;编辑非默认时区/超时的任务时自动展开", async () => {
    mounted = mountPanel();
    await flush();

    clickButton(mounted.container, "注册定时任务");
    expect(mounted.container.querySelector('input[aria-label="时区"]')).toBeNull();
    expect(mounted.container.querySelector('input[aria-label="单次执行超时（秒）"]')).toBeNull();
    clickButton(mounted.container, "高级设置");
    expect(mounted.container.querySelector('input[aria-label="时区"]')).not.toBeNull();
    clickButton(mounted.container, "取消");

    const custom = job({
      id: "j-3",
      sessionId: CONV_ID,
      name: "自定义时区",
      timezone: "Asia/Shanghai",
      timeout: 0,
    });
    mocks.list.mockResolvedValue([custom]);
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();

    clickRowButton(mounted.container, "自定义时区", "编辑");
    const tz = mounted.container.querySelector<HTMLInputElement>('input[aria-label="时区"]');
    expect(tz?.value).toBe("Asia/Shanghai");
  });

  it("编辑空字符串时区（等同默认 UTC）的任务时不自动展开高级设置", async () => {
    const emptyTz = job({
      id: "j-4",
      sessionId: CONV_ID,
      name: "空时区任务",
      timezone: "",
      timeout: 0,
    });
    mocks.list.mockResolvedValue([emptyTz]);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "空时区任务", "编辑");
    expect(mounted.container.querySelector('input[aria-label="时区"]')).toBeNull();
    expect(mounted.container.querySelector('input[aria-label="单次执行超时（秒）"]')).toBeNull();
  });

  it("注册被拒时展示后端原因并保留表单", async () => {
    mocks.register.mockRejectedValue({
      code: "bad_param",
      message: "invalid cron schedule",
    });
    mounted = mountPanel();
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
    expect(mocks.list).toHaveBeenCalledTimes(1);
  });

  it("任务卡片文案：全角冒号、待执行时间点", async () => {
    const noNext = job({ id: "j-3", sessionId: CONV_ID, nextRunAt: "" });
    mocks.list.mockResolvedValue([noNext]);
    mounted = mountPanel();
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("没有待执行的时间点");
    expect(text).not.toContain("对账");
  });

  it("模型档案选择器：默认项显式指向当前激活档案，列出全部档案且不暴露密钥", async () => {
    mounted = mountPanel();
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
    expect(labels.some((l) => l.includes("同步档案 · example-model（密钥已保存）"))).toBe(true);
    expect(labels.join("\n")).not.toContain("sk-real-key");
    expect(mocks.modelOverview).toHaveBeenCalled();
  });

  it("注册时把所选模型档案随任务持久化", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-9", sessionId: CONV_ID }));
    mounted = mountPanel();
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
      sessionId: CONV_ID,
      prompt: "truncate old logs",
      schedule: "0 4 * * *",
      timezone: "UTC",
      modelProfileId: "p-other",
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已注册");
  });

  it("编辑任务：预填当前值并披露重建语义，保存时先建停用替身、删旧后按原状态启用", async () => {
    const withProfile = job({
      id: "j-1",
      sessionId: CONV_ID,
      name: "磁盘巡检",
      modelProfileId: "p-other",
    });
    mocks.list.mockResolvedValue([withProfile]);
    mocks.register.mockResolvedValue(
      job({ id: "j-10", sessionId: CONV_ID, modelProfileId: "p-active", enabled: false }),
    );
    mocks.setEnabled.mockResolvedValue(
      job({ id: "j-10", sessionId: CONV_ID, modelProfileId: "p-active", enabled: true }),
    );
    mounted = mountPanel();
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
      sessionId: CONV_ID,
      name: "磁盘巡检",
      prompt: "check disk",
      schedule: "0 2 * * *",
      timezone: "UTC",
      timeoutMs: 60_000,
      modelProfileId: "p-active",
      disabled: true,
    });
    expect(mocks.unregister).toHaveBeenCalledWith(CONV_ID, "j-1");
    expect(mocks.setEnabled).toHaveBeenCalledWith(CONV_ID, "j-10", true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("编辑已停用任务时保持停用状态", async () => {
    const disabledJob = job({ id: "j-2", sessionId: CONV_ID, enabled: false });
    mocks.list.mockResolvedValue([disabledJob]);
    mocks.register.mockResolvedValue(job({ id: "j-11", sessionId: CONV_ID, enabled: false }));
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "check disk", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: CONV_ID,
      prompt: "check disk",
      schedule: "0 2 * * *",
      timezone: "UTC",
      timeoutMs: 60_000,
      disabled: true,
    });
    expect(mocks.unregister).toHaveBeenCalledWith(CONV_ID, "j-2");
    expect(mocks.setEnabled).not.toHaveBeenCalled();
  });

  it("旧任务注销失败（如保存期间开始执行）：回滚清理新任务，不留双任务", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: CONV_ID, enabled: false }));
    mocks.unregister
      .mockRejectedValueOnce(new Error("任务正在执行，不能注销"))
      .mockResolvedValueOnce(undefined);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledTimes(1);
    expect(mocks.register).toHaveBeenCalledWith(expect.objectContaining({ disabled: true }));
    expect(mocks.unregister).toHaveBeenNthCalledWith(1, CONV_ID, "j-1");
    expect(mocks.unregister).toHaveBeenNthCalledWith(2, CONV_ID, "j-10");
    expect(mocks.setEnabled).not.toHaveBeenCalled();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("旧任务注销未成功");
    expect(text).toContain("已清理重建的新任务");
    expect(text).toContain("任务正在执行，不能注销");
    expect(mounted.container.querySelector('textarea[aria-label="任务提示词"]')).not.toBeNull();
    expect(mocks.toast).not.toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("回滚也失败：持久人工处理告警，涉及任务禁止编辑，冲突解决后告警消除", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: CONV_ID, enabled: false }));
    mocks.unregister.mockRejectedValue(new Error("存储故障"));
    const both = [JOB_A, job({ id: "j-10", sessionId: CONV_ID, enabled: false })];
    mocks.list.mockResolvedValue(both);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("请手动注销其中一条");
    expect(text).toContain(`${CONV_ID}/j-1`);
    expect(text).toContain(`${CONV_ID}/j-10`);
    expect(text).toContain("存储故障");
    const editDisabledFor = (rowText: string) =>
      [...mounted!.container.querySelectorAll("button")].find(
        (b) =>
          b.textContent?.trim() === "编辑" &&
          b.parentElement?.parentElement?.textContent?.includes(rowText),
      )?.disabled;
    expect(editDisabledFor("磁盘巡检")).toBe(true);
    expect(editDisabledFor("check disk")).toBe(true);

    mocks.list.mockResolvedValue([JOB_A]);
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    expect(mounted.container.textContent).not.toContain("请手动注销其中一条");
  });

  it("读取失败不算冲突已解决，告警保留", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: CONV_ID, enabled: false }));
    mocks.unregister.mockRejectedValue(new Error("存储故障"));
    const both = [JOB_A, job({ id: "j-10", sessionId: CONV_ID, enabled: false })];
    mocks.list.mockResolvedValue(both);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();
    expect(mounted.container.textContent).toContain("请手动注销其中一条");

    mocks.list.mockRejectedValue(new Error("磁盘炸了"));
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("磁盘炸了");
    expect(text).toContain("请手动注销其中一条");

    mocks.list.mockResolvedValue(both);
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    expect(mounted.container.textContent).toContain("请手动注销其中一条");

    mocks.list.mockResolvedValue([JOB_A]);
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    expect(mounted.container.textContent).not.toContain("请手动注销其中一条");
  });

  it("行内启停 pending 时禁止保存；完成后按最新状态提交", async () => {
    const slowToggle = deferred<TestJob>();
    mocks.setEnabled.mockReturnValue(slowToggle.promise);
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: CONV_ID, enabled: false }));
    mounted = mountPanel();
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

    slowToggle.resolve(job({ id: "j-1", sessionId: CONV_ID, name: "磁盘巡检", enabled: false }));
    await flush();
    const saveAfter = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存",
    );
    expect(saveAfter?.disabled).toBe(false);
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith(expect.objectContaining({ disabled: true }));
    expect(mocks.unregister).toHaveBeenCalledWith(CONV_ID, "j-1");
    expect(mocks.setEnabled).not.toHaveBeenCalledWith(CONV_ID, "j-10", true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("替换后启用失败：新任务保持停用并如实提示", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: CONV_ID, enabled: false }));
    mocks.setEnabled.mockRejectedValue(new Error("存储故障"));
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.setEnabled).toHaveBeenCalledWith(CONV_ID, "j-10", true);
    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("任务已重建但启用失败：存储故障"),
    );
    expect(mounted.container.querySelector('textarea[aria-label="任务提示词"]')).toBeNull();
  });

  it("保存进行中锁定该行的启停、注销与编辑", async () => {
    const slow = deferred<TestJob>();
    mocks.register.mockReturnValue(slow.promise);
    mounted = mountPanel();
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

    slow.resolve(job({ id: "j-10", sessionId: CONV_ID }));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("编辑打开期间该行禁止注销与重复编辑，启停仍可用", async () => {
    mounted = mountPanel();
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
      sessionId: CONV_ID,
      name: "磁盘巡检",
      run: { id: "r-1", scheduledFor: "", startedAt: "", deadline: "" },
    });
    mocks.list.mockResolvedValue([running]);
    mounted = mountPanel();
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
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain("跟随激活档案「生产档案」");
  });

  it("任务保存的档案已删除时如实标注，编辑时给出未知档案选项与警告", async () => {
    const orphan = job({
      id: "j-1",
      sessionId: CONV_ID,
      name: "磁盘巡检",
      modelProfileId: "p-ghost",
    });
    mocks.list.mockResolvedValue([orphan]);
    mounted = mountPanel();
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

  it("档案读取失败时如实提示，任务列表与注册不受影响", async () => {
    mocks.modelOverview.mockRejectedValue(new Error("档案服务不可用"));
    const withProfile = job({
      id: "j-1",
      sessionId: CONV_ID,
      name: "磁盘巡检",
      modelProfileId: "p-other",
    });
    mocks.list.mockResolvedValue([withProfile]);
    mocks.register.mockResolvedValue(job({ id: "j-9", sessionId: CONV_ID }));
    mounted = mountPanel();
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
      sessionId: CONV_ID,
      prompt: "do something",
      schedule: "0 1 * * *",
      timezone: "UTC",
    });
  });

  it("编辑表单在档案读取失败时显示状态未知，而非误报已删除", async () => {
    mocks.modelOverview.mockRejectedValue(new Error("档案服务不可用"));
    const withProfile = job({
      id: "j-1",
      sessionId: CONV_ID,
      name: "磁盘巡检",
      modelProfileId: "p-other",
    });
    mocks.list.mockResolvedValue([withProfile]);
    mounted = mountPanel();
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
    mounted = mountPanel();
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
    mounted = mountPanel();
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
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain("跟随激活档案");

    mocks.modelOverview.mockResolvedValue(OVERVIEW);
    act(() => useUi.getState().bumpModelProfilesRevision());
    await flush();
    expect(mounted.container.textContent).toContain("跟随激活档案「生产档案」");
    expect(mocks.modelOverview).toHaveBeenCalledTimes(2);
  });

  it("窄屏结构：长任务名断行不溢出，模型档案选择器行可折行", async () => {
    const longName = job({
      id: "j-1",
      sessionId: CONV_ID,
      name: "r120-磁盘巡检任务名称故意取得很长用来验证窄屏折行",
    });
    mocks.list.mockResolvedValue([longName]);
    mounted = mountPanel();
    await flush();

    const nameSpan = [...mounted.container.querySelectorAll("span")].find(
      (s) => s.textContent === longName.name,
    );
    expect(nameSpan).toBeTruthy();
    expect(nameSpan!.className).toContain("break-words");

    clickButton(mounted.container, "注册定时任务");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect(select).toBeTruthy();
    const row = select.closest("div")!;
    expect(row.className).toContain("flex-wrap");
    expect(select.className).toContain("min-w-0");
    expect(select.className).toContain("flex-1");
    expect(select.value).toBe("");
  });
});
