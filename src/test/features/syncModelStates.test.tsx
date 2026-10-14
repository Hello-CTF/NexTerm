/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    linkGet: vi.fn(),
    linkSet: vi.fn(),
    status: vi.fn(),
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
      linkGet: mocks.linkGet,
      linkSet: mocks.linkSet,
      status: mocks.status,
    },
    modelApi: {
      overview: mocks.overview,
      presets: mocks.presets,
      save: mocks.save,
      activate: vi.fn(),
      remove: vi.fn(),
      refresh: vi.fn(),
      preset: vi.fn(),
      usageSummary: vi.fn().mockResolvedValue([]),
    },
    sessionApi: {},
    terminalApi: {},
    vaultApi: {},
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remoteLinkApi: { ...actual.remoteLinkApi, linkSet: mocks.linkSet },
  };
});

import { SyncCard } from "../../features/settings/SyncCard";
import { ModelPanel } from "../../features/ai/ModelPanel";
import { ModelManager } from "../../features/ai/ModelPanel";
import { useUi } from "../../app/store";

const EMPTY_LINK = {
  url: "",
  username: "",
  insecure: false,
  hasPassword: false,
  verifiedAt: 0,
  lastError: null as string | null,
};
const SAVED_LINK = {
  url: "https://sync.example.com",
  username: "alice",
  insecure: false,
  hasPassword: true,
  verifiedAt: 1,
  lastError: null as string | null,
};

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.linkGet.mockResolvedValue(EMPTY_LINK);
  mocks.linkSet.mockResolvedValue(SAVED_LINK);
  mocks.status.mockResolvedValue({
    configured: false,
    loggedIn: false,
    seq: 0,
    verifiedAt: 0,
    lastError: "",
  });
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

describe("SyncCard desktop states", () => {
  it("reports sync configuration read failures inline and supports retry", async () => {
    mocks.linkGet.mockRejectedValueOnce(new Error("corrupt configuration"));
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => text().includes("同步配置读取失败 · corrupt configuration"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => !text().includes("同步配置读取失败"));
    expect(mocks.linkGet).toHaveBeenCalledTimes(2);
  });

  it("repopulates the saved link URL, username, and insecure setting while keeping the password blank", async () => {
    mocks.linkGet.mockResolvedValue(SAVED_LINK);
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => (mounted!.container.querySelector<HTMLInputElement>("#sync-url")?.value ?? "") !== "");

    const urlInput = mounted.container.querySelector<HTMLInputElement>("#sync-url")!;
    const userInput = mounted.container.querySelector<HTMLInputElement>("#sync-user")!;
    const passInput = mounted.container.querySelector<HTMLInputElement>("#sync-pass")!;
    expect(urlInput.value).toBe("https://sync.example.com");
    expect(userInput.value).toBe("alice");
    expect(passInput.value).toBe("");
    // Advanced options are collapsed when skip-cert-verify is off
    expect(mounted.container.querySelector('input[type="checkbox"]')).toBeNull();
    clickButton(mounted.container, "高级选项");
    await flush();
    const insecureCheckbox = mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    expect(insecureCheckbox.checked).toBe(false);
  });

  it("auto-expands advanced options when skip-certificate-verification is enabled", async () => {
    mocks.linkGet.mockResolvedValue({ ...SAVED_LINK, insecure: true });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => (mounted!.container.querySelector<HTMLInputElement>("#sync-url")?.value ?? "") !== "");
    await flushUntil(() => mounted!.container.querySelector('input[type="checkbox"]') !== null);

    const insecureCheckbox = mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    expect(insecureCheckbox.checked).toBe(true);
  });

  it("calls linkSet with the username and password on login, completing sync setup in one step", async () => {
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => mounted!.container.querySelector("#sync-url") !== null);
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-url")!, "https://sync.example.com");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-user")!, "alice");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-pass")!, "correct horse battery staple");
    await flushUntil(() => {
      const btn = [...mounted!.container.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "登录",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(mounted!.container, "登录");
    await flushUntil(() => mocks.linkSet.mock.calls.length > 0);
    expect(mocks.linkSet).toHaveBeenCalledWith(
      expect.objectContaining({
        url: "https://sync.example.com",
        username: "alice",
        password: "correct horse battery staple",
      }),
    );
    expect(text()).toContain("登录后，数据会自动在本机与云端之间同步");
    expect(text()).not.toContain("立即同步");
  });

  it("requires password input before enabling login when no password has been saved", async () => {
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => mounted!.container.querySelector("#sync-url") !== null);
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-url")!, "https://sync.example.com");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-user")!, "alice");
    await flush();
    const btn = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "登录",
    ) as HTMLButtonElement | undefined;
    expect(btn?.disabled).toBe(true);
  });

  it("does not render the account card on desktop, leaving one login button and no dead account login entry", async () => {
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => mounted!.container.querySelector("#sync-url") !== null);
    const loginButtons = [...mounted!.container.querySelectorAll("button")].filter(
      (b) => b.textContent?.trim() === "登录",
    );
    expect(loginButtons.length).toBe(1);
    expect(text()).not.toContain("当前未登录");
  });
});

describe("ModelManager states", () => {
  it("shows an error and retry instead of the empty-profile state when the profile list fails to load", async () => {
    mocks.overview.mockRejectedValueOnce(new Error("configuration unreadable"));
    mounted = mount(createElement(ModelManager));
    await flushUntil(() => text().includes("档案列表加载失败 · configuration unreadable"));
    expect(text()).not.toContain("还没有模型档案");
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("还没有模型档案"));
    expect(mocks.overview).toHaveBeenCalledTimes(2);
  });

  it("retains the previous list and shows a refresh error with retry when reload fails after initial success", async () => {
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
    await flushUntil(() =>
      [...mounted!.container.querySelectorAll(".nx-menu-item")].some((item) =>
        item.textContent?.includes("DeepSeek"),
      ),
    );

    mocks.overview.mockRejectedValueOnce(new Error("disk unreadable"));
    clickButton(mounted!.container, "保存");
    await flushUntil(() => text().includes("档案列表刷新失败 · disk unreadable"));
    expect(text()).toContain("DeepSeek");
    expect(text()).not.toContain("还没有模型档案");

    mocks.overview.mockResolvedValueOnce({ profiles: [saved], activeId: "p1" });
    clickButton(mounted!.container, "重试");
    await flushUntil(() => !text().includes("档案列表刷新失败"));
    expect(text()).toContain("DeepSeek");
    expect(mocks.overview).toHaveBeenCalledTimes(3);
  });

  it("shows the newly saved profile instead of the empty state when the initial list is empty, saving succeeds, and reload fails", async () => {
    mocks.overview.mockResolvedValueOnce({ profiles: [], activeId: null });
    mounted = mount(createElement(ModelManager));
    await flushUntil(() => text().includes("还没有模型档案"));

    mocks.overview.mockRejectedValueOnce(new Error("disk unreadable"));
    mocks.save.mockImplementation(async (p: { id: string; name: string }) => ({
      ...p,
      id: "new-id",
    }));
    click(mounted!.container.querySelector('button[title="新增档案"]')!);
    await flush();
    const nameLabel = [...mounted!.container.querySelectorAll("label")].find(
      (l) => l.textContent?.trim() === "显示名称",
    );
    const nameInput = mounted!.container.querySelector<HTMLInputElement>(
      `input[id="${nameLabel!.htmlFor}"]`,
    );
    setInputValue(nameInput!, "new profile");
    clickButton(mounted!.container, "保存");
    await flushUntil(() => text().includes("档案列表刷新失败 · disk unreadable"));
    const listHasNew = [...mounted!.container.querySelectorAll(".nx-menu-item")].some((b) =>
      b.textContent?.includes("new profile"),
    );
    expect(listHasNew, "After saving, the list must contain the new profile, not the empty state").toBe(true);
    expect(text()).not.toContain("还没有模型档案");

    mocks.overview.mockResolvedValueOnce({
      profiles: [
        {
          id: "new-id",
          name: "new profile",
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
    expect(text()).toContain("new profile");
  });
});

describe("ModelPanel modal keyboard behavior", () => {
  function modalEl(): HTMLElement {
    const el = mounted!.container.querySelector<HTMLElement>(".nx-modal");
    if (!el) throw new Error("modal not found");
    return el;
  }

  it("closes on Escape when there are no unsaved changes", async () => {
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

  it("asks to discard unsaved changes before closing on Escape", async () => {
    const onClose = vi.fn();
    mocks.ask.mockResolvedValueOnce(false);
    mounted = mount(createElement(ModelPanel, { onClose }));
    await flushUntil(() => text().includes("模型配置"));
    const newBtn = mounted!.container.querySelector<HTMLButtonElement>('button[title="新增档案"]');
    expect(newBtn).not.toBeNull();
    act(() => newBtn!.click());
    await flush();
    const nameInput = [...mounted!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.trim() === "显示名称",
    );
    const input = mounted!.container.querySelector<HTMLInputElement>(
      `input[id="${nameInput!.htmlFor}"]`,
    );
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "draft");
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
