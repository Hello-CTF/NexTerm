/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    digest: vi.fn(),
    linkGet: vi.fn(),
    token: vi.fn(),
    linkSet: vi.fn(),
    remoteDigest: vi.fn(),
    push: vi.fn(),
    pull: vi.fn(),
    overview: vi.fn(),
    presets: vi.fn(),
    save: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: {
      digest: mocks.digest,
      linkGet: mocks.linkGet,
      token: mocks.token,
      linkSet: mocks.linkSet,
      remoteDigest: mocks.remoteDigest,
      push: mocks.push,
      pull: mocks.pull,
      rotateToken: vi.fn(),
    },
    modelApi: {
      overview: mocks.overview,
      presets: mocks.presets,
      save: mocks.save,
      activate: vi.fn(),
      remove: vi.fn(),
      refresh: vi.fn(),
      preset: vi.fn(),
    },
    sessionApi: {},
    terminalApi: {},
    vaultApi: {},
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { SyncCard } from "../../features/settings/SyncCard";
import { ModelPanel } from "../../features/ai/ModelPanel";
import { ModelManager } from "../../features/ai/ModelPanel";
import { useUi } from "../../app/store";

const EMPTY_DIGEST = { origin: "local", assets: [] };
const SAVED_LINK = {
  url: "https://sync.example.com",
  tokenKind: "server",
  token: "saved-token",
  insecure: false,
  verifiedAt: 1,
  lastError: null as string | null,
};
const REMOTE_ASSET = {
  id: "a1",
  name: "web-01",
  kind: "ssh",
  host: "1.2.3.4",
  updatedAt: 1,
  deletedAt: null,
  hasCred: false,
};

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.digest.mockResolvedValue(EMPTY_DIGEST);
  mocks.linkGet.mockResolvedValue(null);
  mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
  mocks.presets.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [], appDialog: null });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

async function waitForTestButtonEnabled() {
  await flushUntil(() => {
    const btn = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存并测试连接",
    ) as HTMLButtonElement | undefined;
    return !!btn && !btn.disabled;
  });
}

describe("SyncCard 状态", () => {
  it("本机摘要读取失败时内联报错并可重试", async () => {
    mocks.digest.mockRejectedValueOnce(new Error("数据库被锁"));
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => text().includes("本机资产摘要读取失败 · 数据库被锁"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => !text().includes("本机资产摘要读取失败"));
    expect(mocks.digest).toHaveBeenCalledTimes(2);
  });

  it("同步配置读取失败时内联报错并可重试", async () => {
    mocks.linkGet.mockRejectedValueOnce(new Error("配置损坏"));
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => text().includes("同步配置读取失败 · 配置损坏"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => !text().includes("同步配置读取失败"));
    expect(mocks.linkGet).toHaveBeenCalledTimes(2);
  });

  it("访问令牌 label 关联到输入框，且令牌框关闭自动填充", async () => {
    mounted = withClient(createElement(SyncCard));
    await flush();
    const label = [...mounted!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.includes("访问令牌"),
    );
    expect(label).toBeTruthy();
    const input = mounted!.container.querySelector<HTMLInputElement>(
      `input[id="${label!.htmlFor}"]`,
    );
    expect(input).not.toBeNull();
    expect(input!.autocomplete).toBe("off");
  });

  it("本机摘要失败 + 对端为空：不生成对照表，也不显示「两边都没有」", async () => {
    mocks.digest.mockRejectedValue(new Error("数据库被锁"));
    mocks.linkGet.mockResolvedValue(SAVED_LINK);
    mocks.linkSet.mockImplementation(async (l: typeof SAVED_LINK) => l);
    mocks.remoteDigest.mockResolvedValue({ origin: "remote", assets: [] });
    mounted = withClient(createElement(SyncCard));
    await waitForTestButtonEnabled();
    clickButton(mounted!.container, "保存并测试连接");
    await flushUntil(() =>
      mocks.toast.mock.calls.some((c) => String(c[1]).includes("已连接（对端标识")),
    );
    expect(text()).not.toContain("资产对照");
    expect(text()).not.toContain("两边都还没有可同步的资产");
    expect(text()).toContain("本机资产摘要读取失败");
  });

  it("本机摘要失败 + 对端非空：不生成差异对照，不允许基于未知本机状态同步", async () => {
    mocks.digest.mockRejectedValue(new Error("数据库被锁"));
    mocks.linkGet.mockResolvedValue(SAVED_LINK);
    mocks.linkSet.mockImplementation(async (l: typeof SAVED_LINK) => l);
    mocks.remoteDigest.mockResolvedValue({ origin: "remote", assets: [REMOTE_ASSET] });
    mounted = withClient(createElement(SyncCard));
    await waitForTestButtonEnabled();
    clickButton(mounted!.container, "保存并测试连接");
    await flushUntil(() =>
      mocks.toast.mock.calls.some((c) => String(c[1]).includes("已连接（对端标识")),
    );
    expect(text()).not.toContain("资产对照");
    expect(text()).not.toContain("仅对端");
    expect(text()).not.toContain("推送到对端");
    expect(text()).toContain("本机资产摘要读取失败");
  });

  it("重新读取对端失败保留最后成功的对端数据，错误上方可重试恢复", async () => {
    mocks.linkGet.mockResolvedValue(SAVED_LINK);
    mocks.linkSet.mockImplementation(async (l: typeof SAVED_LINK) => l);
    mocks.remoteDigest
      .mockResolvedValueOnce({ origin: "remote", assets: [REMOTE_ASSET] })
      .mockRejectedValueOnce(new Error("对端离线"))
      .mockResolvedValueOnce({ origin: "remote", assets: [REMOTE_ASSET] });
    mounted = withClient(createElement(SyncCard));
    await waitForTestButtonEnabled();
    clickButton(mounted!.container, "保存并测试连接");
    await flushUntil(() => text().includes("web-01"));

    clickButton(mounted!.container, "重新读取对端");
    await flushUntil(() => text().includes("对端离线"));
    expect(text()).toContain("web-01");

    clickButton(mounted!.container, "重试");
    await flushUntil(() => !text().includes("对端离线"));
    expect(text()).toContain("web-01");
    expect(mocks.remoteDigest).toHaveBeenCalledTimes(3);
  });

  it("初次配置读取失败后，保存并成功重读配置会清除旧错误", async () => {
    mocks.linkGet.mockRejectedValueOnce(new Error("配置损坏"));
    mocks.linkGet.mockResolvedValue(SAVED_LINK);
    mocks.linkSet.mockImplementation(async (l: typeof SAVED_LINK) => l);
    mocks.remoteDigest.mockResolvedValue({ origin: "remote", assets: [] });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => text().includes("同步配置读取失败 · 配置损坏"));

    const urlInput = mounted!.container.querySelector<HTMLInputElement>('input[id="sync-url"]');
    const tokenInput = mounted!.container.querySelector<HTMLInputElement>('input[id="sync-token"]');
    expect(urlInput).not.toBeNull();
    expect(tokenInput).not.toBeNull();
    setInputValue(urlInput!, "https://sync.example.com");
    setInputValue(tokenInput!, "fresh-token");
    clickButton(mounted!.container, "保存并测试连接");
    await flushUntil(() => text().includes("已连接"));
    expect(text()).not.toContain("同步配置读取失败");
    expect(mocks.linkGet).toHaveBeenCalledTimes(2);
  });
});

describe("ModelManager 状态", () => {
  it("档案列表读取失败时显示错误与重试，而不是「还没有模型档案」", async () => {
    mocks.overview.mockRejectedValueOnce(new Error("配置不可读"));
    mounted = mount(createElement(ModelManager));
    await flushUntil(() => text().includes("档案列表加载失败 · 配置不可读"));
    expect(text()).not.toContain("还没有模型档案");
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("还没有模型档案"));
    expect(mocks.overview).toHaveBeenCalledTimes(2);
  });

  it("首次成功后 reload 失败保留旧列表并显示刷新错误与重试", async () => {
    const saved = {
      id: "p1",
      name: "DeepSeek",
      baseUrl: "https://api.deepseek.com/v1",
      apiKey: "key",
      model: "deepseek-chat",
      temperature: 0.3,
      contextWindow: 32768,
      proxy: null,
      stream: true,
      fallbackModel: null,
    };
    mocks.overview.mockResolvedValueOnce({ profiles: [saved], activeId: "p1" });
    mocks.save.mockImplementation(async (p: typeof saved) => ({ ...p }));
    mounted = mount(createElement(ModelManager));
    await flushUntil(() => text().includes("DeepSeek"));

    mocks.overview.mockRejectedValueOnce(new Error("磁盘不可读"));
    clickButton(mounted!.container, "保存");
    await flushUntil(() => text().includes("档案列表刷新失败 · 磁盘不可读"));
    expect(text()).toContain("DeepSeek");
    expect(text()).not.toContain("还没有模型档案");

    mocks.overview.mockResolvedValueOnce({ profiles: [saved], activeId: "p1" });
    clickButton(mounted!.container, "重试");
    await flushUntil(() => !text().includes("档案列表刷新失败"));
    expect(text()).toContain("DeepSeek");
    expect(mocks.overview).toHaveBeenCalledTimes(3);
  });

  it("初始为空 → 保存成功 → reload 失败：列表显示新档案，不出现「还没有模型档案」", async () => {
    mocks.overview.mockResolvedValueOnce({ profiles: [], activeId: null });
    mounted = mount(createElement(ModelManager));
    await flushUntil(() => text().includes("还没有模型档案"));

    mocks.overview.mockRejectedValueOnce(new Error("磁盘不可读"));
    mocks.save.mockImplementation(async (p: { id: string; name: string }) => ({
      ...p,
      id: "new-id",
    }));
    click(mounted!.container.querySelector('button[title="新增档案"]')!);
    await flush();
    const nameLabel = [...mounted!.container.querySelectorAll("label")].find(
      (l) => l.textContent?.trim() === "展示名",
    );
    const nameInput = mounted!.container.querySelector<HTMLInputElement>(
      `input[id="${nameLabel!.htmlFor}"]`,
    );
    setInputValue(nameInput!, "新档案");
    clickButton(mounted!.container, "保存");
    await flushUntil(() => text().includes("档案列表刷新失败 · 磁盘不可读"));
    const listHasNew = [...mounted!.container.querySelectorAll(".nx-menu-item")].some((b) =>
      b.textContent?.includes("新档案"),
    );
    expect(listHasNew, "保存成功后列表必须包含新档案，而非空态").toBe(true);
    expect(text()).not.toContain("还没有模型档案");

    mocks.overview.mockResolvedValueOnce({
      profiles: [
        {
          id: "new-id",
          name: "新档案",
          baseUrl: "",
          apiKey: "",
          model: "",
          temperature: 0.3,
          contextWindow: 32768,
          proxy: null,
          stream: true,
          fallbackModel: null,
        },
      ],
      activeId: null,
    });
    clickButton(mounted!.container, "重试");
    await flushUntil(() => !text().includes("档案列表刷新失败"));
    expect(text()).toContain("新档案");
  });
});

describe("ModelPanel 弹层键盘行为", () => {
  function modalEl(): HTMLElement {
    const el = mounted!.container.querySelector<HTMLElement>(".nx-modal");
    if (!el) throw new Error("modal not found");
    return el;
  }

  it("Escape 触发关闭（无未保存修改时直接关闭）", async () => {
    const onClose = vi.fn();
    mounted = mount(createElement(ModelPanel, { onClose }));
    await flushUntil(() => text().includes("模型配置"));
    act(() => {
      modalEl().dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      );
    });
    await flushUntil(() => onClose.mock.calls.length > 0);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("有未保存修改时 Escape 先弹放弃确认", async () => {
    const onClose = vi.fn();
    mocks.ask.mockResolvedValueOnce(false);
    mounted = mount(createElement(ModelPanel, { onClose }));
    await flushUntil(() => text().includes("模型配置"));
    const newBtn = mounted!.container.querySelector<HTMLButtonElement>('button[title="新增档案"]');
    expect(newBtn).not.toBeNull();
    act(() => newBtn!.click());
    await flush();
    const nameInput = [...mounted!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.trim() === "展示名",
    );
    const input = mounted!.container.querySelector<HTMLInputElement>(
      `input[id="${nameInput!.htmlFor}"]`,
    );
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "草稿");
      input!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush();
    expect(text()).toContain("有未保存的修改");
    act(() => {
      modalEl().dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      );
    });
    await flush();
    expect(mocks.ask).toHaveBeenCalledOnce();
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("SyncCard 同步报告方向", () => {
  const REPORT_BASE = {
    groupsCreated: 0,
    groupsUpdated: 0,
    assetsCreated: 0,
    assetsUpdated: 0,
    credsCreated: 0,
    credsUpdated: 0,
    skippedNewer: 1,
    refused: 0,
    warnings: [] as string[],
  };

  async function selectComparableRow(): Promise<void> {
    mocks.linkGet.mockResolvedValue(SAVED_LINK);
    mocks.linkSet.mockImplementation(async (l: typeof SAVED_LINK) => l);
    mocks.remoteDigest.mockResolvedValue({ origin: "remote", assets: [REMOTE_ASSET] });
    mocks.digest.mockResolvedValue({ origin: "local", assets: [REMOTE_ASSET] });
    mounted = withClient(createElement(SyncCard));
    await waitForTestButtonEnabled();
    clickButton(mounted!.container, "保存并测试连接");
    await flushUntil(() => text().includes("web-01"));
    const row = [...mounted!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.includes("web-01"),
    );
    click(row!.querySelector('input[type="checkbox"]')!);
  }

  it("推送报告：skippedNewer 是对端较新", async () => {
    mocks.push.mockResolvedValue({ ...REPORT_BASE });
    await selectComparableRow();
    clickButton(mounted!.container, "推送到对端 (1)");
    await flushUntil(() => text().includes("推送结果"));
    expect(text()).toContain("跳过（对端较新） 1");
    expect(text()).not.toContain("跳过（本机较新）");
  });

  it("拉取报告：skippedNewer 是本机较新", async () => {
    mocks.pull.mockResolvedValue({ ...REPORT_BASE });
    await selectComparableRow();
    clickButton(mounted!.container, "从对端拉取 (1)");
    await flushUntil(() => text().includes("拉取结果"));
    expect(text()).toContain("跳过（本机较新） 1");
    expect(text()).not.toContain("跳过（对端较新）");
  });
});
