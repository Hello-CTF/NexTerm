/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, mount, type MountedView } from "./reactTestUtils";

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

const MASKED_KEY = "••••••••••••";
const REAL_KEY = "sk-real-key-material";

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
    apiKey: REAL_KEY,
    model: "example-model",
    temperature: 0.3,
    contextWindow: 32768,
    proxy: null,
    stream: true,
    ...overrides,
  };
}

const PROFILE_MASKED = profile({ id: "p-masked", name: "脱敏档案", apiKey: MASKED_KEY });
const PROFILE_EMPTY = profile({ id: "p-empty", name: "无密钥档案", apiKey: "" });

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

describe("CronPanel masked key presentation", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: mocks.toast });
    mocks.list.mockResolvedValue([]);
    mocks.modelOverview.mockResolvedValue({
      profiles: [PROFILE_MASKED, PROFILE_EMPTY],
      activeId: "p-empty",
    });
    mocks.register.mockResolvedValue(job({ id: "j-new", sessionId: "c-1" }));
    mocks.setEnabled.mockResolvedValue(undefined);
    mocks.unregister.mockResolvedValue(undefined);
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("档案选择器把脱敏密钥如实标注为已保存，而非不可用", async () => {
    mounted = mount(createElement(CronPanel, { conversationId: "c-1", onClose: mocks.onClose }));
    await flush();
    clickButton(mounted.container, "新建定时任务");
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="模型档案"]',
    )!;
    const labels = [...select.options].map((o) => o.textContent ?? "");
    const maskedOption = labels.find((l) => l.includes("脱敏档案 · example-model"));
    expect(maskedOption).toContain("脱敏档案 · example-model（密钥已保存）");
    expect(maskedOption).not.toContain("密钥不可用");
    const emptyOption = labels.find((l) => l.includes("无密钥档案 · example-model"));
    expect(emptyOption).toContain("无密钥档案 · example-model");
    expect(emptyOption).not.toContain("密钥已保存");
    expect(emptyOption).not.toContain("密钥不可用");
    expect(labels.join("\n")).not.toContain(MASKED_KEY);
    expect(labels.join("\n")).not.toContain(REAL_KEY);
  });

  it("任务行把脱敏密钥档案标注为密钥已保存", async () => {
    mocks.list.mockResolvedValue([
      job({ id: "j-1", sessionId: "c-1", name: "磁盘巡检", modelProfileId: "p-masked" }),
    ]);
    mounted = mount(createElement(CronPanel, { conversationId: "c-1", onClose: mocks.onClose }));
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("档案密钥已保存");
    expect(text).toContain("档案「脱敏档案」");
    expect(text).not.toContain("档案密钥不可用");
    expect(text).not.toContain(MASKED_KEY);
    expect(text).not.toContain(REAL_KEY);
  });

  it("无密钥档案的任务行既不标注已保存也不标注不可用", async () => {
    mocks.list.mockResolvedValue([
      job({ id: "j-1", sessionId: "c-1", name: "磁盘巡检", modelProfileId: "p-empty" }),
    ]);
    mounted = mount(createElement(CronPanel, { conversationId: "c-1", onClose: mocks.onClose }));
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("档案「无密钥档案」");
    expect(text).not.toContain("档案密钥已保存");
    expect(text).not.toContain("档案密钥不可用");
  });

  it("未知档案仍如实标注档案已删除，且不标注密钥已保存", async () => {
    mocks.list.mockResolvedValue([
      job({ id: "j-1", sessionId: "c-1", name: "磁盘巡检", modelProfileId: "p-ghost" }),
    ]);
    mounted = mount(createElement(CronPanel, { conversationId: "c-1", onClose: mocks.onClose }));
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("档案已删除");
    expect(text).toContain("档案 p-ghost");
    expect(text).not.toContain("档案密钥已保存");
    expect(text).not.toContain("档案密钥不可用");
  });

  it("档案读取失败时保持状态未知，不误报已保存或已删除", async () => {
    mocks.modelOverview.mockRejectedValue(new Error("档案服务不可用"));
    mocks.list.mockResolvedValue([
      job({ id: "j-1", sessionId: "c-1", name: "磁盘巡检", modelProfileId: "p-masked" }),
    ]);
    mounted = mount(createElement(CronPanel, { conversationId: "c-1", onClose: mocks.onClose }));
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("档案 p-masked");
    expect(text).not.toContain("档案已删除");
    expect(text).not.toContain("档案密钥已保存");
    expect(text).not.toContain("档案密钥不可用");
  });
});
