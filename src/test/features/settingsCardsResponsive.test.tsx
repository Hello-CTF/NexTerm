/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, clickButton, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    memorySettings: vi.fn(),
    memoryIndex: vi.fn(),
    memoryGet: vi.fn(),
    memorySetSettings: vi.fn(),
    knownHostList: vi.fn(),
    conversationList: vi.fn(),
    cronList: vi.fn(),
    modelOverview: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/memory", () => ({
  memoryApi: {
    settings: mocks.memorySettings,
    index: mocks.memoryIndex,
    get: mocks.memoryGet,
    create: vi.fn(),
    edit: vi.fn(),
    delete: vi.fn(),
    setSettings: mocks.memorySetSettings,
  },
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      knownHostList: mocks.knownHostList,
      knownHostRemove: vi.fn(),
    },
    aiApi: {
      conversationList: mocks.conversationList,
    },
    modelApi: {
      overview: mocks.modelOverview,
    },
  };
});
vi.mock("../../ipc/cron", () => ({
  cronApi: {
    list: mocks.cronList,
    register: vi.fn(),
    setEnabled: vi.fn(),
    unregister: vi.fn(),
  },
  cronTimeoutMs: (job: { timeout: number }) => Math.round(job.timeout / 1_000_000),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { MemoryCard } from "../../features/settings/MemoryCard";
import { KnownHostsCard } from "../../features/settings/KnownHostsCard";
import { CronCard } from "../../features/settings/CronCard";
import { useUi } from "../../app/store";
import type { MemoryTopicIndex } from "../../ipc/memory";

const SCOPE = { tenant: "local", subject: "default" };
const ULID = "01J4ZQXK6M2N8P3R5T7V9W0Y1Z";
const LONG_TOKEN = `tok_${"a1B2".repeat(24)}`;

const INDEX: MemoryTopicIndex[] = [
  {
    topic: "operations-long-topic-name-for-narrow-layout",
    entries: [{ id: ULID, version: 3, redacted: true, updatedAt: 1728000000000 }],
  },
];

const KNOWN_HOST = {
  id: "kh1",
  host: "r120-audit-host-with-a-very-long-hostname.example-internal.company.com",
  port: 2222,
  keyType: "ssh-ed25519",
  fingerprint: `SHA256:${"b".repeat(43)}`,
  addedAt: 1728000000000,
};

const CRON_JOB = {
  id: "j1",
  sessionId: "c1",
  name: "r120-磁盘巡检任务名称故意取得很长用来验证窄屏折行",
  prompt: "检查各分区磁盘使用率并汇报",
  schedule: "0 2 * * *",
  timezone: "UTC",
  enabled: true,
  timeout: 60000000000,
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
  revision: 1,
  nextRunAt: "2026-10-05T02:00:00Z",
  lease: {},
  run: { id: "", scheduledFor: "", startedAt: "", deadline: "" },
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.memorySettings.mockResolvedValue({ injectionEnabled: false, toolsEnabled: false, version: 3 });
  mocks.memoryIndex.mockResolvedValue(INDEX);
  mocks.memoryGet.mockResolvedValue({
    id: ULID,
    topic: "operations",
    content: `重启安排在 02:00；令牌 ${LONG_TOKEN} 不换行会溢出`,
    version: 3,
    redacted: true,
    createdAt: 1,
    updatedAt: 2,
  });
  mocks.knownHostList.mockResolvedValue([KNOWN_HOST]);
  mocks.conversationList.mockResolvedValue([{ id: "c1", title: "审查会话" }]);
  mocks.cronList.mockResolvedValue([CRON_JOB]);
  mocks.modelOverview.mockResolvedValue({ profiles: [], activeId: null });
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("MemoryCard 窄屏结构", () => {
  it("ULID 截断但保留完整值入口，行可折行", async () => {
    mounted = mount(createElement(MemoryCard));
    await flushUntil(() => mounted!.container.textContent?.includes(ULID) ?? false);
    const idSpan = [...mounted.container.querySelectorAll("span")].find(
      (s) => s.textContent === ULID,
    );
    expect(idSpan).toBeTruthy();
    expect(idSpan!.className).toContain("truncate");
    expect(idSpan!.getAttribute("title")).toBe(ULID);
    const row = idSpan!.closest("button");
    expect(row?.className).toContain("flex-wrap");
    expect(row?.className).toContain("min-w-0");
    expect(mounted.container.textContent).toContain("已脱敏");
    expect(mounted.container.textContent).toContain("v3 ·");
  });

  it("展开正文 pre 对长 token 断行", async () => {
    mounted = mount(createElement(MemoryCard));
    await flushUntil(() => mounted!.container.textContent?.includes(ULID) ?? false);
    const row = [...mounted.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes(ULID),
    );
    click(row!);
    await flushUntil(() => mounted!.container.querySelector("pre") !== null);
    const pre = mounted.container.querySelector("pre");
    expect(pre?.className).toContain("break-words");
    expect(pre?.textContent).toContain(LONG_TOKEN);
  });

  it("两个开关行整行是 label，点文字即切换", async () => {
    mounted = mount(createElement(MemoryCard));
    await flushUntil(() => {
      const input = mounted!.container.querySelector<HTMLInputElement>(
        'input[aria-label="注入到 AI 运行"]',
      );
      return !!input && !input.disabled;
    });
    for (const name of ["注入到 AI 运行", "允许模型使用记忆工具"]) {
      const input = mounted.container.querySelector<HTMLInputElement>(
        `input[aria-label="${name}"]`,
      );
      expect(input?.closest("label")).toBeTruthy();
    }
    const label = mounted.container
      .querySelector('input[aria-label="注入到 AI 运行"]')!
      .closest("label")!;
    click(label);
    await flushUntil(() => mocks.memorySetSettings.mock.calls.length > 0);
    expect(mocks.memorySetSettings).toHaveBeenCalledWith(SCOPE, 3, { injectionEnabled: true });
  });
});

describe("KnownHostsCard 窄屏结构", () => {
  it("长主机名截断且 title 带完整值，keyType 与撤销按钮常驻", async () => {
    mounted = mount(createElement(KnownHostsCard));
    await flushUntil(() => mounted!.container.textContent?.includes(KNOWN_HOST.host) ?? false);
    const hostSpan = [...mounted.container.querySelectorAll("span")].find((s) =>
      s.textContent?.includes(KNOWN_HOST.host),
    );
    expect(hostSpan).toBeTruthy();
    expect(hostSpan!.className).toContain("truncate");
    expect(hostSpan!.getAttribute("title")).toBe(`${KNOWN_HOST.host}:${KNOWN_HOST.port}`);
    expect(mounted.container.textContent).toContain("ssh-ed25519");
    const fp = [...mounted.container.querySelectorAll("div")].find((d) =>
      d.textContent === KNOWN_HOST.fingerprint,
    );
    expect(fp?.className).toContain("truncate");
    expect(fp?.getAttribute("title")).toBe(KNOWN_HOST.fingerprint);
    const revoke = [...mounted.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("撤销信任"),
    );
    expect(revoke).toBeTruthy();
    expect(revoke!.className).toContain("shrink-0");
  });
});

describe("CronCard 窄屏结构", () => {
  it("长任务名断行不溢出", async () => {
    mounted = mount(createElement(CronCard));
    await flushUntil(() => mounted!.container.textContent?.includes(CRON_JOB.name) ?? false);
    const nameSpan = [...mounted.container.querySelectorAll("span")].find(
      (s) => s.textContent === CRON_JOB.name,
    );
    expect(nameSpan).toBeTruthy();
    expect(nameSpan!.className).toContain("break-words");
  });

  it("模型档案选择器行可折行，下拉可收缩不撑破卡片", async () => {
    mounted = mount(createElement(CronCard));
    await flushUntil(() => mounted!.container.textContent?.includes(CRON_JOB.name) ?? false);
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
