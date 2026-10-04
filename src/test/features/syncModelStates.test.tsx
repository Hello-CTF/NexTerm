/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { clickButton, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    digest: vi.fn(),
    linkGet: vi.fn(),
    token: vi.fn(),
    linkSet: vi.fn(),
    remoteDigest: vi.fn(),
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
      push: vi.fn(),
      pull: vi.fn(),
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
