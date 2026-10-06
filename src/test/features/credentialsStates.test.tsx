/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { readFileSync } from "node:fs";
import { act } from "react";
import { click, clickButton, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  vaultStatus: vi.fn(),
  listCredentials: vi.fn(),
  assetList: vi.fn(),
  setCredential: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    vaultApi: {
      status: mocks.vaultStatus,
      listCredentials: mocks.listCredentials,
      setCredential: mocks.setCredential,
      lock: vi.fn(),
      unlock: vi.fn(),
      revealCredential: vi.fn(),
      updateCredential: vi.fn(),
      deleteCredential: vi.fn(),
    },
    assetApi: {
      list: mocks.assetList,
    },
    sessionApi: {},
    terminalApi: {},
  };
});

import { CredentialsSidebar } from "../../features/credentials/CredentialsSidebar";
import { CredentialsPanel } from "../../features/credentials/CredentialsPanel";
import { CredentialsView } from "../../features/credentials/CredentialsView";
import { NewCredentialModal } from "../../features/credentials/NewCredentialModal";
import { useUi } from "../../app/store";

const UNUSED_CRED = {
  id: "c1",
  name: "db-prod",
  kind: "password",
  usedBy: [],
  createdAt: 1,
  updatedAt: 1,
};

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false, mode: "off" });
  mocks.listCredentials.mockResolvedValue([]);
  mocks.assetList.mockResolvedValue([]);
  useUi.setState({
    pushToast: mocks.toast,
    sessions: [],
    leftOpen: true,
    leftWidth: 260,
    appDialog: null,
  });
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

describe("CredentialsSidebar 状态", () => {
  it("列表失败时给出真实错误与重试，而不是「还没有任何凭据」", async () => {
    mocks.listCredentials.mockRejectedValue(new Error("保险库打不开"));
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("凭据列表加载失败"));
    expect(text()).toContain("保险库打不开");
    expect(text()).not.toContain("还没有任何凭据");
  });

  it("重试后恢复，真空才显示「还没有任何凭据」", async () => {
    mocks.listCredentials.mockRejectedValueOnce(new Error("保险库打不开"));
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("凭据列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("还没有任何凭据"));
  });

  it("「未使用」使用语义 warning 前景", async () => {
    mocks.listCredentials.mockResolvedValue([UNUSED_CRED]);
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("未使用"));
    const badge = [...mounted!.container.querySelectorAll("span")].find(
      (s) => s.textContent === "未使用",
    );
    expect(badge?.className).toContain("var(--nx-fg-warning)");
  });

  it("筛选 chip 选中态用 accent 色板，未选中 hover 用中性色反馈", async () => {
    mocks.listCredentials.mockResolvedValue([
      UNUSED_CRED,
      { id: "c2", name: "deploy-key", kind: "private_key", usedBy: [], createdAt: 1, updatedAt: 1 },
    ]);
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("deploy-key"));
    const chips = [...mounted!.container.querySelectorAll("button[aria-pressed]")];
    expect(chips.length).toBe(3);
    const all = chips.find((b) => b.textContent?.includes("全部"))!;
    const key = chips.find((b) => b.textContent?.includes("私钥"))!;
    expect(all.getAttribute("aria-pressed")).toBe("true");
    expect(all.className).toContain("var(--color-accent)");
    expect(all.className).not.toContain("bg-white/");
    expect(key.getAttribute("aria-pressed")).toBe("false");
    expect(key.className).toContain("hover:bg-neutral-800/70");
    expect(key.className).not.toContain("var(--color-accent)");
  });

  it("点击筛选 chip 切换 aria-pressed，再次点击恢复全部", async () => {
    mocks.listCredentials.mockResolvedValue([
      UNUSED_CRED,
      { id: "c2", name: "deploy-key", kind: "private_key", usedBy: [], createdAt: 1, updatedAt: 1 },
    ]);
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("deploy-key"));
    const chip = (label: string) =>
      [...mounted!.container.querySelectorAll("button[aria-pressed]")].find((b) =>
        b.textContent?.includes(label),
      )!;
    expect(chip("全部").getAttribute("aria-pressed")).toBe("true");
    click(chip("私钥"));
    await flushUntil(() => !text().includes("db-prod"));
    expect(chip("私钥").getAttribute("aria-pressed")).toBe("true");
    expect(chip("私钥").className).toContain("var(--color-accent)");
    expect(chip("全部").getAttribute("aria-pressed")).toBe("false");
    click(chip("私钥"));
    await flushUntil(() => text().includes("db-prod"));
    expect(chip("私钥").getAttribute("aria-pressed")).toBe("false");
  });

  it("加载中带 role=status 语义，而不是静默纯文本", async () => {
    mocks.listCredentials.mockReturnValue(new Promise(() => {}));
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('[role="status"]'));
    expect(mounted!.container.querySelector('[role="status"]')?.textContent).toContain("加载中");
  });

  it("真空态 sidebar 与 panel 统一为「还没有任何凭据」", async () => {
    mocks.listCredentials.mockResolvedValue([]);
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("还没有任何凭据"));
    mounted.unmount();
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("还没有任何凭据"));
  });
});

describe("CredentialsPanel 状态", () => {
  it("列表失败时显示错误与重试，而不是「还没有任何凭据」", async () => {
    mocks.listCredentials.mockRejectedValue(new Error("保险库打不开"));
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("凭据列表加载失败"));
    expect(text()).not.toContain("还没有任何凭据");
  });

  it("重试后恢复真空态", async () => {
    mocks.listCredentials.mockRejectedValueOnce(new Error("保险库打不开"));
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("凭据列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("还没有任何凭据"));
  });

  it("加载中带 role=status 语义", async () => {
    mocks.listCredentials.mockReturnValue(new Promise(() => {}));
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => !!mounted!.container.querySelector('[role="status"]'));
    expect(mounted!.container.querySelector('[role="status"]')?.textContent).toContain("凭据加载中");
  });
});

describe("CredentialsView 状态", () => {
  it("任一查询失败时显示错误与重试，而不是渲染空快照", async () => {
    mocks.listCredentials.mockRejectedValue(new Error("保险库打不开"));
    mounted = withClient(createElement(CredentialsView, { view: "text", onChange: () => {} }));
    await flushUntil(() => text().includes("加载失败 · 保险库打不开"));
    expect(text()).not.toContain("# NexTerm 凭据视图");
  });

  it("重试后渲染只读快照", async () => {
    mocks.listCredentials.mockRejectedValueOnce(new Error("保险库打不开"));
    mounted = withClient(createElement(CredentialsView, { view: "text", onChange: () => {} }));
    await flushUntil(() => text().includes("加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("# NexTerm 凭据视图"));
  });

  it("加载中带 role=status 语义", async () => {
    mocks.listCredentials.mockReturnValue(new Promise(() => {}));
    mocks.assetList.mockReturnValue(new Promise(() => {}));
    mounted = withClient(createElement(CredentialsView, { view: "text", onChange: () => {} }));
    await flushUntil(() => !!mounted!.container.querySelector('[role="status"]'));
    expect(mounted!.container.querySelector('[role="status"]')?.textContent).toContain(
      "凭据视图加载中",
    );
  });
});

describe("NewCredentialModal 表单与键盘", () => {
  function mountModal() {
    const onClose = vi.fn();
    const onSaved = vi.fn();
    mounted = mount(createElement(NewCredentialModal, { onClose, onSaved }));
    return { onClose, onSaved };
  }

  function modalEl(): HTMLElement {
    const el = mounted!.container.querySelector<HTMLElement>(".nx-modal");
    if (!el) throw new Error("modal not found");
    return el;
  }

  it("把可见 label 关联到对应控件", () => {
    mountModal();
    const doc = mounted!.container;
    const nameLabel = [...doc.querySelectorAll("label")].find((l) => l.textContent?.trim() === "名称");
    expect(nameLabel).toBeTruthy();
    const nameInput = doc.querySelector<HTMLInputElement>(`input[id="${nameLabel!.htmlFor}"]`);
    expect(nameInput).not.toBeNull();

    const valueLabel = [...doc.querySelectorAll("label")].find((l) => l.textContent?.trim() === "值");
    expect(valueLabel).toBeTruthy();
    const valueInput = doc.querySelector<HTMLInputElement>(`input[id="${valueLabel!.htmlFor}"]`);
    expect(valueInput).not.toBeNull();
    expect(valueInput!.type).toBe("password");
    expect(valueInput!.autocomplete).toBe("off");
  });

  it("打开时焦点落在名称输入框", () => {
    mountModal();
    const doc = mounted!.container;
    const nameLabel = [...doc.querySelectorAll("label")].find((l) => l.textContent?.trim() === "名称");
    const nameInput = doc.querySelector<HTMLInputElement>(`input[id="${nameLabel!.htmlFor}"]`);
    expect(document.activeElement).toBe(nameInput);
  });

  it("Escape 关闭弹层", () => {
    const { onClose } = mountModal();
    act(() => {
      modalEl().dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      );
    });
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("Tab 在弹层内循环（焦点陷阱）", () => {
    mountModal();
    const focusables = [
      ...modalEl().querySelectorAll<HTMLElement>("button:not([disabled]), input:not([disabled])"),
    ].filter((el) => el.tabIndex >= 0);
    const last = focusables[focusables.length - 1];
    const first = focusables[0];
    act(() => last.focus());
    expect(document.activeElement).toBe(last);
    act(() => {
      modalEl().dispatchEvent(
        new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true }),
      );
    });
    expect(document.activeElement).toBe(first);
  });

  it("role=dialog + aria-modal + 标题关联", () => {
    mountModal();
    const modal = modalEl();
    expect(modal.getAttribute("role")).toBe("dialog");
    expect(modal.getAttribute("aria-modal")).toBe("true");
    const labelledBy = modal.getAttribute("aria-labelledby");
    expect(labelledBy).toBeTruthy();
    expect(modal.querySelector(`[id="${labelledBy}"]`)?.textContent).toContain("新建凭据");
  });
});

describe("凭据 amber 文字语义化（源码守卫）", () => {
  const files = [
    "CredentialsSidebar.tsx",
    "CredentialsPanel.tsx",
    "CredentialsView.tsx",
    "NewCredentialModal.tsx",
  ];
  for (const file of files) {
    it(`${file} 不再使用低对比度 amber 文字类`, () => {
      const src = readFileSync(`src/features/credentials/${file}`, "utf8");
      expect(src).not.toMatch(/text-amber-(400|500)\b/);
    });
  }
});
