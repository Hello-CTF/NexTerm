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
  retryAt?: string;
  circuitOpenUntil?: string;
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
  afterEach(async () => {
    await flush();
    mounted?.unmount();
    mounted = undefined;
  });

  it("reads and displays only the current conversation's scheduled jobs", async () => {
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

  it("shows the bound host name and unattended command guidance", async () => {
    mounted = mount(
      createElement(CronPanel, {
        conversationId: CONV_ID,
        hostName: "prod-host",
        onClose: mocks.onClose,
      }),
    );
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("绑定主机：prod-host");
    expect(text).toContain("不会新建终端");
    expect(text).toContain("明确要求 AI 直接执行命令");
  });

  it("reloads jobs for the new conversation after switching conversations", async () => {
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

  it("shows the current conversation's empty-state hint when the list is empty", async () => {
    mocks.list.mockResolvedValue([]);
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain("这个会话还没有定时任务");
  });

  it("shows loading errors and allows retry", async () => {
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

  it("keeps the refresh error instead of the stale list and recovers after retry", async () => {
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

  it("discards late results from an older list request", async () => {
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

  it("disables a job in the current conversation and updates it in place", async () => {
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

  it("reports enable/disable failures and reloads the list", async () => {
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

  it("asks before deletion and does nothing when rejected", async () => {
    mocks.ask.mockResolvedValue(false);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "删除");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("磁盘巡检");
    expect(question).not.toContain("所属会话");
    expect(mocks.unregister).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("reloads after confirmed deletion and removes the job", async () => {
    mocks.ask.mockResolvedValue(true);
    let jobs: TestJob[] = [JOB_A];
    mocks.list.mockImplementation(() => Promise.resolve(jobs));
    mocks.unregister.mockImplementation(() => {
      jobs = [];
      return Promise.resolve(undefined);
    });
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "删除");
    await flush();
    expect(mocks.unregister).toHaveBeenCalledWith(CONV_ID, "j-1");
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已删除");
    expect(mounted.container.textContent).not.toContain("磁盘巡检");
    expect(mounted.container.textContent).toContain("这个会话还没有定时任务");
  });

  it("keeps the job and reports deletion failures", async () => {
    mocks.ask.mockResolvedValue(true);
    mocks.unregister.mockRejectedValue(new Error("Read-only database"));
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "删除");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("删除失败"));
    expect(mounted.container.textContent).toContain("磁盘巡检");
  });

  it("omits the conversation selector from the create form and uses the current conversation automatically", async () => {
    mounted = mountPanel();
    await flush();

    clickButton(mounted.container, "新建定时任务");
    expect(mounted.container.querySelector('select[aria-label="所属 AI 会话"]')).toBeNull();
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="任务名称"]')!,
      "Clean logs",
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
    clickButton(mounted.container, "创建");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: CONV_ID,
      name: "Clean logs",
      prompt: "truncate old logs",
      schedule: "0 4 * * *",
      timezone: "UTC",
      timeoutMs: 120_000,
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已创建");
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("collapses advanced settings by default for create and for edits with non-default timezone or timeout until expanded manually", async () => {
    mounted = mountPanel();
    await flush();

    clickButton(mounted.container, "新建定时任务");
    expect(mounted.container.querySelector('input[aria-label="时区"]')).toBeNull();
    expect(mounted.container.querySelector('input[aria-label="单次执行超时（秒）"]')).toBeNull();
    clickButton(mounted.container, "高级设置");
    expect(mounted.container.querySelector('input[aria-label="时区"]')).not.toBeNull();
    clickButton(mounted.container, "取消");

    const custom = job({
      id: "j-3",
      sessionId: CONV_ID,
      name: "Custom timezone",
      timezone: "Asia/Shanghai",
      timeout: 0,
    });
    mocks.list.mockResolvedValue([custom]);
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();

    clickRowButton(mounted.container, "Custom timezone", "编辑");
    expect(mounted.container.querySelector('input[aria-label="时区"]')).toBeNull();

    clickButton(mounted.container, "高级设置");
    const tz = mounted.container.querySelector<HTMLInputElement>('input[aria-label="时区"]');
    expect(tz?.value).toBe("Asia/Shanghai");
  });

  it("does not auto-expand advanced settings when editing a job whose empty timezone is equivalent to UTC", async () => {
    const emptyTz = job({
      id: "j-4",
      sessionId: CONV_ID,
      name: "Empty timezone job",
      timezone: "",
      timeout: 0,
    });
    mocks.list.mockResolvedValue([emptyTz]);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "Empty timezone job", "编辑");
    expect(mounted.container.querySelector('input[aria-label="时区"]')).toBeNull();
    expect(mounted.container.querySelector('input[aria-label="单次执行超时（秒）"]')).toBeNull();
  });

  it("shows the backend rejection reason and preserves the form when creation fails", async () => {
    mocks.register.mockRejectedValue({
      code: "bad_param",
      message: "invalid cron schedule",
    });
    mounted = mountPanel();
    await flush();

    clickButton(mounted.container, "新建定时任务");
    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "do something",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="cron 表达式"]')!,
      "not a cron",
    );
    clickButton(mounted.container, "创建");
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("invalid cron schedule");
    expect(mounted.container.querySelector('textarea[aria-label="任务提示词"]')).not.toBeNull();
    expect(mocks.list).toHaveBeenCalledTimes(1);
  });

  it("uses a full-width colon and shows the pending execution time on task cards", async () => {
    const noNext = job({ id: "j-3", sessionId: CONV_ID, nextRunAt: "" });
    mocks.list.mockResolvedValue([noNext]);
    mounted = mountPanel();
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("暂无待执行时间");
    expect(text).not.toContain("对账");
  });

  it("points the model profile default at the active profile, lists every profile, and never exposes keys", async () => {
    mounted = mountPanel();
    await flush();
    clickButton(mounted.container, "新建定时任务");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect(select.value).toBe("");
    const labels = [...select.options].map((o) => o.textContent ?? "");
    expect(labels[0]).toContain("跟随当前档案「生产档案」");
    expect(labels.some((l) => l.includes("生产档案 · example-model"))).toBe(true);
    expect(labels.some((l) => l.includes("备用档案 · backup-model"))).toBe(true);
    expect(labels.some((l) => l.includes("同步档案 · example-model（密钥已保存）"))).toBe(true);
    expect(labels.join("\n")).not.toContain("sk-real-key");
    expect(mocks.modelOverview).toHaveBeenCalled();
  });

  it("persists the selected model profile with a newly created job", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-9", sessionId: CONV_ID }));
    mounted = mountPanel();
    await flush();

    clickButton(mounted.container, "新建定时任务");
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
    clickButton(mounted.container, "创建");
    await flush();

    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: CONV_ID,
      prompt: "truncate old logs",
      schedule: "0 4 * * *",
      timezone: "UTC",
      modelProfileId: "p-other",
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已创建");
  });

  it("prefills current values, discloses replacement semantics, creates a disabled replacement, deletes the old job, and restores the original enabled state", async () => {
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
    expect(text).toContain("任务标识和执行历史不会保留");

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

  it("keeps a disabled job disabled while editing it", async () => {
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

  it("rolls back the new job when deleting the old job fails, such as when it starts running during save, without leaving duplicates", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: CONV_ID, enabled: false }));
    mocks.unregister
      .mockRejectedValueOnce(new Error("任务正在执行，不能删除"))
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
    expect(text).toContain("旧任务删除未成功");
    expect(text).toContain("已清理重建的新任务");
    expect(text).toContain("任务正在执行，不能删除");
    expect(mounted.container.querySelector('textarea[aria-label="任务提示词"]')).not.toBeNull();
    expect(mocks.toast).not.toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("keeps a persistent manual-resolution warning when rollback also fails, disables editing for affected jobs, and clears it after resolution", async () => {
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
    expect(text).toContain("请手动删除其中一条");
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
    expect(mounted.container.textContent).not.toContain("请手动删除其中一条");
  });

  it("does not treat a read failure as conflict resolution and keeps the warning", async () => {
    mocks.register.mockResolvedValue(job({ id: "j-10", sessionId: CONV_ID, enabled: false }));
    mocks.unregister.mockRejectedValue(new Error("存储故障"));
    const both = [JOB_A, job({ id: "j-10", sessionId: CONV_ID, enabled: false })];
    mocks.list.mockResolvedValue(both);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    clickButton(mounted.container, "保存");
    await flush();
    expect(mounted.container.textContent).toContain("请手动删除其中一条");

    mocks.list.mockRejectedValue(new Error("磁盘炸了"));
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("磁盘炸了");
    expect(text).toContain("请手动删除其中一条");

    mocks.list.mockResolvedValue(both);
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    expect(mounted.container.textContent).toContain("请手动删除其中一条");

    mocks.list.mockResolvedValue([JOB_A]);
    click(mounted.container.querySelector('button[title="刷新"]')!);
    await flush();
    expect(mounted.container.textContent).not.toContain("请手动删除其中一条");
  });

  it("blocks saving while the row enable/disable action is pending and submits the latest state after it completes", async () => {
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

  it("keeps the replacement disabled and reports the error when enabling it fails", async () => {
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

  it("locks enable/disable, delete, and edit actions for a row while saving", async () => {
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
    expect(rowButton("删除")?.disabled).toBe(true);
    expect(rowButton("编辑")?.disabled).toBe(true);

    slow.resolve(job({ id: "j-10", sessionId: CONV_ID }));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("success", "定时任务已更新");
  });

  it("blocks deletion and repeated editing while an edit is open but still allows enable/disable", async () => {
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    const rowButton = (label: string) =>
      [...mounted!.container.querySelectorAll("button")].find(
        (b) =>
          b.textContent?.trim() === label &&
          b.parentElement?.parentElement?.textContent?.includes("磁盘巡检"),
      );
    expect(rowButton("删除")?.disabled).toBe(true);
    expect(rowButton("删除")?.getAttribute("title")).toBe("编辑中，请先保存或取消编辑");
    expect(rowButton("编辑")?.disabled).toBe(true);
    expect(rowButton("停用")?.disabled).toBe(false);
  });

  it("does not allow editing a running job", async () => {
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

  it("shows that a job without an assigned profile follows the current profile", async () => {
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain("跟随当前档案「生产档案」");
  });

  it("marks a deleted saved profile and offers an unknown-profile option and warning while editing", async () => {
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

  it("reports profile-loading failures without affecting the job list or creation", async () => {
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

    clickButton(mounted.container, "新建定时任务");
    expect(mounted.container.textContent).toContain("模型档案读取失败：档案服务不可用");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect([...select.options].map((o) => o.textContent?.trim())).toEqual([
      "跟随当前档案（读取失败，状态未知）",
    ]);

    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "do something",
    );
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[aria-label="cron 表达式"]')!,
      "0 1 * * *",
    );
    clickButton(mounted.container, "创建");
    await flush();
    expect(mocks.register).toHaveBeenCalledWith({
      sessionId: CONV_ID,
      prompt: "do something",
      schedule: "0 1 * * *",
      timezone: "UTC",
    });
  });

  it("shows unknown profile status in the edit form when profile loading fails instead of claiming deletion", async () => {
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
    expect(labels[0]).toContain("跟随当前档案（读取失败，状态未知）");
    expect(
      labels.some((l) => l.includes("已存档案（读取失败，状态未知）") && l.includes("p-other")),
    ).toBe(true);
  });

  it("shows the loading state in the profile default option and updates it after loading completes", async () => {
    const slow = deferred<typeof OVERVIEW>();
    mocks.modelOverview.mockReturnValue(slow.promise);
    mounted = mountPanel();
    await flush();

    clickButton(mounted.container, "新建定时任务");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    expect([...select.options][0]?.textContent).toContain("跟随当前档案（读取中…）");

    slow.resolve(OVERVIEW);
    await flush();
    expect([...select.options][0]?.textContent).toContain("跟随当前档案「生产档案」");
  });

  it("rejects creation and reports it when the selected profile is found to be deleted before submission", async () => {
    mounted = mountPanel();
    await flush();

    clickButton(mounted.container, "新建定时任务");
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
    clickButton(mounted.container, "创建");
    await flush();

    expect(mocks.register).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("所选模型档案已不存在");
  });

  it("refreshes profile data after the profile revision increments", async () => {
    mocks.modelOverview.mockResolvedValue({ profiles: [], activeId: null });
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain("跟随当前档案");

    mocks.modelOverview.mockResolvedValue(OVERVIEW);
    act(() => useUi.getState().bumpModelProfilesRevision());
    await flush();
    expect(mounted.container.textContent).toContain("跟随当前档案「生产档案」");
    expect(mocks.modelOverview).toHaveBeenCalledTimes(2);
  });

  it("wraps long job names without overflowing narrow screens and allows the model profile row to wrap", async () => {
    const longName = job({
      id: "j-1",
      sessionId: CONV_ID,
      name: "r120-intentionally-long-disk-inspection-job-name-for-narrow-screen-wrapping",
    });
    mocks.list.mockResolvedValue([longName]);
    mounted = mountPanel();
    await flush();

    const nameSpan = [...mounted.container.querySelectorAll("span")].find(
      (s) => s.textContent === longName.name,
    );
    expect(nameSpan).toBeTruthy();
    expect(nameSpan!.className).toContain("break-words");

    clickButton(mounted.container, "新建定时任务");
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

  it("uses the latest of schedule, retry, and circuit-breaker times for next execution and prioritizes disabled state without showing pending execution", async () => {
    const scheduled = job({
      id: "j-scheduled",
      sessionId: CONV_ID,
      name: "Latest time",
      nextRunAt: "2030-01-01T02:00:00Z",
      retryAt: "2031-02-03T04:00:00Z",
      circuitOpenUntil: "2032-04-05T06:00:00Z",
    });
    mocks.list.mockResolvedValue([scheduled]);
    mounted = mountPanel();
    await flush();
    expect(mounted.container.textContent).toContain(
      `下次执行 ${new Date("2032-04-05T06:00:00Z").toLocaleString()}`,
    );
    expect(mounted.container.textContent).toContain("启停和删除仅影响当前任务");
    expect(mounted.container.textContent).toContain("删除所属 AI 会话时，其全部定时任务也会删除");

    const disabled = job({
      id: "j-disabled",
      sessionId: CONV_ID,
      name: "Disabled job",
      enabled: false,
      nextRunAt: "2030-01-01T02:00:00Z",
      retryAt: "2031-02-03T04:00:00Z",
      circuitOpenUntil: "2032-04-05T06:00:00Z",
      run: { id: "running-1", scheduledFor: "", startedAt: "", deadline: "" },
    });
    mocks.list.mockResolvedValue([disabled]);
    mounted.unmount();
    mounted = mountPanel();
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("已停用");
    expect(text).toContain("暂无待执行时间");
    const badges = [...mounted.container.querySelectorAll(".nx-badge")].map((badge) => badge.textContent);
    expect(badges).not.toContain("执行中");
    expect(badges).not.toContain("已暂停");
    expect(text).not.toContain("下次执行");
  });

  it("requires confirmation for unsaved edits before switching jobs, canceling, or closing the panel", async () => {
    const other = job({ id: "j-other", sessionId: CONV_ID, name: "Backup job", prompt: "backup" });
    mocks.list.mockResolvedValue([JOB_A, other]);
    mocks.ask.mockResolvedValue(false);
    mounted = mountPanel();
    await flush();

    clickRowButton(mounted.container, "磁盘巡检", "编辑");
    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')!,
      "dirty draft",
    );
    clickRowButton(mounted.container, "Backup job", "编辑");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')?.value).toBe("dirty draft");

    click(mounted.container.querySelector('button[title="关闭"]')!);
    await flush();
    expect(mocks.onClose).not.toHaveBeenCalled();

    clickButton(mounted.container, "取消");
    await flush();
    expect(mounted.container.querySelector('textarea[aria-label="任务提示词"]')).not.toBeNull();

    mocks.ask.mockResolvedValue(true);
    clickRowButton(mounted.container, "Backup job", "编辑");
    await flush();
    expect(mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="任务提示词"]')?.value).toBe("backup");

    click(mounted.container.querySelector('button[title="关闭"]')!);
    await flush();
    expect(mocks.onClose).toHaveBeenCalledTimes(1);
  });
});
