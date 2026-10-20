/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { click, clickButton, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  vaultStatus: vi.fn(),
  listCredentials: vi.fn(),
  assetList: vi.fn(),
  setCredential: vi.fn(),
  unlock: vi.fn(),
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
      unlock: mocks.unlock,
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
  mocks.unlock.mockResolvedValue(undefined);
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

function expectNoLowContrastAmberText(container: HTMLElement): void {
  const rendered = [...container.querySelectorAll("*")].flatMap((element) => [...element.classList]);
  expect(rendered.filter((name) => /(?:^|:)text-amber-(400|500)(?:\/|$)/.test(name))).toEqual([]);
}

describe("CredentialsSidebar states", () => {
  it("list failure shows the real error with a retry, not the empty state", async () => {
    mocks.listCredentials.mockRejectedValue(new Error("vault unavailable"));
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("凭据列表加载失败"));
    expect(text()).toContain("vault unavailable");
    expect(text()).not.toContain("还没有任何凭据");
  });

  it("after retry it recovers; only a truly empty vault shows the empty state", async () => {
    mocks.listCredentials.mockRejectedValueOnce(new Error("vault unavailable"));
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("凭据列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("还没有任何凭据"));
  });

  it("the Unused badge uses the semantic warning foreground", async () => {
    mocks.listCredentials.mockResolvedValue([UNUSED_CRED]);
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("未使用"));
    const badge = [...mounted!.container.querySelectorAll("span")].find(
      (s) => s.textContent === "未使用",
    );
    expect(badge?.className).toContain("var(--nx-fg-warning)");
    expectNoLowContrastAmberText(mounted!.container);
  });

  it("filter chips use the accent palette when selected and neutral hover feedback when unselected", async () => {
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

  it("clicking a filter chip toggles aria-pressed; clicking again restores All", async () => {
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

  it("loading exposes role=status semantics instead of silent plain text", async () => {
    mocks.listCredentials.mockReturnValue(new Promise(() => {}));
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('[role="status"]'));
    expect(mounted!.container.querySelector('[role="status"]')?.textContent).toContain("加载中");
  });

  it("sidebar and panel share the same empty state", async () => {
    mocks.listCredentials.mockResolvedValue([]);
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => text().includes("还没有任何凭据"));
    mounted.unmount();
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("还没有任何凭据"));
  });
});

describe("CredentialsPanel states", () => {
  it("list failure shows the error with a retry, not the empty state", async () => {
    mocks.listCredentials.mockRejectedValue(new Error("vault unavailable"));
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("凭据列表加载失败"));
    expect(text()).not.toContain("还没有任何凭据");
  });

  it("retry recovers to the empty state", async () => {
    mocks.listCredentials.mockRejectedValueOnce(new Error("vault unavailable"));
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("凭据列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("还没有任何凭据"));
  });

  it("loading exposes role=status semantics", async () => {
    mocks.listCredentials.mockReturnValue(new Promise(() => {}));
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => !!mounted!.container.querySelector('[role="status"]'));
    expect(mounted!.container.querySelector('[role="status"]')?.textContent).toContain("凭据加载中");
  });
});

describe("CredentialsPanel vault status badges", () => {
  it("uninitialized shows Not initialized instead of password protection off", async () => {
    mocks.vaultStatus.mockResolvedValue({
      initialized: false,
      mode: "not_init",
      unlocked: false,
      autoLockMinutes: 30,
      passwordless: false,
    });
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("未初始化"));
    expect(text()).not.toContain("未启用密码保护");
    expectNoLowContrastAmberText(mounted!.container);
  });

  it("passwordless vault shows Enabled - no password; the locked state shows no banner and silently auto-unlocks", async () => {
    mocks.vaultStatus.mockResolvedValue({
      initialized: true,
      mode: "master",
      unlocked: false,
      autoLockMinutes: 30,
      passwordless: true,
    });
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("已启用 · 未设密码"));
    expect(text()).not.toContain("凭据库已锁定");
    expect(text()).not.toContain("未启用密码保护");
    await flushUntil(() => mocks.unlock.mock.calls.length > 0);
    expect(mocks.unlock).toHaveBeenCalledWith("");
  });

  it("locked master-password vault shows the Locked badge and unlock banner without auto-unlocking", async () => {
    mocks.vaultStatus.mockResolvedValue({
      initialized: true,
      mode: "master",
      unlocked: false,
      autoLockMinutes: 30,
      passwordless: false,
    });
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() => text().includes("凭据库已锁定"));
    expect(text()).toContain("已锁定");
    expect(mocks.unlock).not.toHaveBeenCalled();
  });
});

describe("NewCredentialModal form and keyboard", () => {
  function mountModal() {
    const onClose = vi.fn();
    const onSaved = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement(NewCredentialModal, { onClose, onSaved }),
      ),
    );
    return { onClose, onSaved };
  }

  function modalEl(): HTMLElement {
    const el = mounted!.container.querySelector<HTMLElement>(".nx-modal");
    if (!el) throw new Error("modal not found");
    return el;
  }

  it("visible labels are associated with their controls", () => {
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
    expectNoLowContrastAmberText(doc);
  });

  it("focus lands on the name input on open", () => {
    mountModal();
    const doc = mounted!.container;
    const nameLabel = [...doc.querySelectorAll("label")].find((l) => l.textContent?.trim() === "名称");
    const nameInput = doc.querySelector<HTMLInputElement>(`input[id="${nameLabel!.htmlFor}"]`);
    expect(document.activeElement).toBe(nameInput);
  });

  it("Escape closes the modal", () => {
    const { onClose } = mountModal();
    act(() => {
      modalEl().dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      );
    });
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("Tab cycles within the modal (focus trap)", () => {
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

  it("role=dialog + aria-modal + labelled-by title", () => {
    mountModal();
    const modal = modalEl();
    expect(modal.getAttribute("role")).toBe("dialog");
    expect(modal.getAttribute("aria-modal")).toBe("true");
    const labelledBy = modal.getAttribute("aria-labelledby");
    expect(labelledBy).toBeTruthy();
    expect(modal.querySelector(`[id="${labelledBy}"]`)?.textContent).toContain("新建凭据");
  });
});
