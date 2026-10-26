/** @vitest-environment jsdom */
// M195 acceptance: settings section navigation (small-tab jump, keyboard reachable) and a unified login entry
// (Web logged-out: the whole settings page has a single login button; account/sync/sharing no longer each place one).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getPermission: vi.fn(),
    setPermission: vi.fn(),
    vaultStatus: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    aiApi: {
      getPermission: mocks.getPermission,
      setPermission: mocks.setPermission,
      testProvider: vi.fn(),
    },
    vaultApi: {
      status: mocks.vaultStatus,
      initDpapi: vi.fn(),
      initMaster: vi.fn(),
      changePassword: vi.fn(),
      setAutoLock: vi.fn(),
    },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), promptText: vi.fn() }));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SECTION_SEARCH, SETTINGS_ITEMS } from "../../features/settings/settingsIndex";
import { SettingsView } from "../../features/settings/SettingsView";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function searchInput(): HTMLInputElement {
  const el = mounted?.container.querySelector<HTMLInputElement>('input[aria-label="搜索设置"]');
  if (!el) throw new Error("settings search box not found");
  return el;
}

function resultLabels(): string[] {
  return [...mounted!.container.querySelectorAll('[role="option"]')].map((o) => o.querySelector("div")!.textContent!);
}

function search(text: string): void {
  setInputValue(searchInput(), text);
}

function key(k: string): void {
  act(() => {
    searchInput().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
  });
}

function groupShown(id: string): boolean {
  return mounted!.container.querySelector<HTMLElement>(`[data-settings-group="${id}"]`)?.hidden === false;
}
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  Element.prototype.scrollIntoView = vi.fn();
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => window.setTimeout(() => cb(0), 0));
  useUi.setState({ pushToast: mocks.toast });
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: null,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "dpapi", unlocked: true, autoLockMinutes: 0 });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAuth.setState({ user: null, dek: null, gate: "ready", pendingRecoveryKey: null, error: null, status: null });
});


describe("Settings search", () => {
  beforeEach(async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
  });

  it("filters sections and individual settings by label", () => {
    search("终端");
    const labels = resultLabels();
    expect(labels).toContain("终端");
    expect(labels).toContain("终端字体");
    expect(labels).not.toContain("拦截规则");
    // Each result names the section it lives in.
    const font = [...mounted!.container.querySelectorAll('[role="option"]')].find((o) => o.textContent?.includes("终端字体"));
    expect(font?.textContent).toContain("外观");
  });

  it("matches Chinese and English synonyms", () => {
    search("shortcut");
    expect(resultLabels()).toContain("快捷键");
    search("快捷键");
    expect(resultLabels()).toContain("快捷键");
    search("font");
    expect(resultLabels()).toEqual(expect.arrayContaining(["终端字体", "界面字号"]));
    search("字体");
    expect(resultLabels()).toContain("终端字体");
    search("credential");
    expect(resultLabels()).toEqual(expect.arrayContaining(["凭据保护", "用密码保护凭据"]));
    search("sync");
    expect(resultLabels()).toContain("账号同步");
    search("主题 theme");
    expect(resultLabels()).toContain("界面主题");
  });

  it("selecting a setting in another group opens it and highlights the setting", async () => {
    search("闲置");
    const option = [...mounted!.container.querySelectorAll<HTMLElement>('[role="option"]')].find((o) =>
      o.textContent?.includes("闲置自动锁定"),
    );
    expect(option).toBeDefined();
    click(option!);
    await flush();
    expect(groupShown("security")).toBe(true);
    expect(groupShown("general")).toBe(false);
    expect(searchInput().value).toBe("");
    // The fixture vault is not password-protected, so the setting itself is not rendered
    // and the section is the only possible target.
    expect(mounted!.container.querySelector('[data-setting="vault-auto-lock"]')).toBeNull();
    const target = document.getElementById("settings-vault")!;
    expect(mounted!.container.querySelectorAll(".nx-setting-flash")).toHaveLength(1);
    expect(target.classList.contains("nx-setting-flash")).toBe(true);
    expect(document.activeElement).toBe(target);
  });

  it("selecting a section result scrolls to that section and highlights it", async () => {
    search("已知主机");
    click(mounted!.container.querySelector<HTMLElement>('[role="option"]')!);
    await flush();
    expect(groupShown("security")).toBe(true);
    const target = document.getElementById("settings-known-hosts")!;
    expect(target.scrollIntoView).toHaveBeenCalled();
    expect(target.classList.contains("nx-setting-flash")).toBe(true);
  });

  it("opens the collapsed advanced panel that holds a setting before highlighting it", async () => {
    expect(mounted!.container.querySelector('[data-setting="terminal-font-family"]')).toBeNull();
    search("终端字体");
    click(mounted!.container.querySelector<HTMLElement>('[role="option"]')!);
    await flush();
    const target = mounted!.container.querySelector<HTMLElement>('[data-setting="terminal-font-family"]');
    expect(target).not.toBeNull();
    expect(mounted!.container.querySelectorAll(".nx-setting-flash")).toHaveLength(1);
    expect(target!.classList.contains("nx-setting-flash")).toBe(true);
    expect(document.activeElement).toBe(target);

    search("选中自动复制");
    click(mounted!.container.querySelector<HTMLElement>('[role="option"]')!);
    await flush();
    const copy = mounted!.container.querySelector<HTMLElement>('[data-setting="copy-on-select"]');
    expect(copy).not.toBeNull();
    expect(copy!.classList.contains("nx-setting-flash")).toBe(true);
  });

  it("announces the result count", () => {
    search("终端");
    const status = mounted!.container.querySelector(".nx-sr-only[role=\"status\"]");
    expect(status?.textContent).toBe(`${resultLabels().length} 个结果`);
    expect(searchInput().getAttribute("aria-expanded")).toBe("true");
    search("zzzz-no-such-setting");
    expect(status?.textContent).toBe("0 个结果");
    expect(searchInput().getAttribute("aria-expanded")).toBe("false");
  });

  it("ignores navigation keys while an IME composition is being committed", () => {
    search("字体");
    const options = () => [...mounted!.container.querySelectorAll('[role="option"]')];
    act(() => {
      searchInput().dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowDown", keyCode: 229, bubbles: true, cancelable: true }),
      );
    });
    expect(options()[0].getAttribute("aria-selected")).toBe("true");
    expect(options()[1].getAttribute("aria-selected")).toBe("false");
  });

  it("shows an empty state when nothing matches", () => {
    search("zzzz-no-such-setting");
    expect(mounted!.container.querySelector('[role="option"]')).toBeNull();
    expect(mounted!.container.textContent).toContain("没有找到与「zzzz-no-such-setting」相关的设置");
  });

  it("restores the full navigation when the query is cleared", () => {
    const toc = () => mounted!.container.querySelector<HTMLElement>('[data-testid="settings-nav-toc"]')!;
    expect(toc().className).not.toContain("!hidden");
    search("终端");
    expect(toc().className).toContain("!hidden");
    search("");
    expect(toc().className).not.toContain("!hidden");
    expect(mounted!.container.querySelector('[role="listbox"]')).toBeNull();
  });

  it("clears the query with Escape", () => {
    search("终端");
    key("Escape");
    expect(searchInput().value).toBe("");
  });

  it("moves through results with the arrow keys and opens the active one with Enter", async () => {
    search("rule");
    expect(resultLabels()).toEqual(["拦截规则"]);
    search("shortcut");
    const before = mounted!.container.querySelectorAll('[role="option"]');
    expect(before[0].getAttribute("aria-selected")).toBe("true");
    search("字体");
    const options = () => [...mounted!.container.querySelectorAll('[role="option"]')];
    expect(options().length).toBeGreaterThan(1);
    key("ArrowDown");
    expect(options()[1].getAttribute("aria-selected")).toBe("true");
    expect(searchInput().getAttribute("aria-activedescendant")).toBe(options()[1].id);
    key("ArrowUp");
    expect(options()[0].getAttribute("aria-selected")).toBe("true");
    key("ArrowUp");
    expect(options().at(-1)!.getAttribute("aria-selected")).toBe("true");
    const label = options().at(-1)!.querySelector("div")!.textContent;
    key("Enter");
    await flush();
    expect(searchInput().value).toBe("");
    expect(label).toBeTruthy();
    expect(mounted!.container.querySelector(".nx-setting-flash")).not.toBeNull();
  });

  it("focuses the search box when / is pressed outside inputs", () => {
    searchInput().blur();
    act(() => {
      mounted!.container.querySelector(".nx-settings-view")!.dispatchEvent(
        new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }),
      );
    });
    return flush().then(() => expect(document.activeElement).toBe(searchInput()));
  });
});

describe("Settings search index", () => {
  it("covers every section and points items at known sections", () => {
    for (const [id, meta] of Object.entries(SECTION_SEARCH)) {
      expect(meta.keywords.length, id).toBeGreaterThan(0);
    }
    for (const item of SETTINGS_ITEMS) {
      expect(SECTION_SEARCH[item.section], item.id).toBeDefined();
    }
    expect(new Set(SETTINGS_ITEMS.map((i) => i.id)).size).toBe(SETTINGS_ITEMS.length);
  });
});

describe("Settings search focus and anchors", () => {
  it("only focuses the search box once the pane is active", async () => {
    mounted = withClient(createElement(SettingsView, { active: false }));
    await flush();
    expect(document.activeElement).not.toBe(searchInput());
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    act(() =>
      mounted!.root.render(
        createElement(QueryClientProvider, { client }, createElement(SettingsView, { active: true })),
      ),
    );
    await flush();
    expect(document.activeElement).toBe(searchInput());
  });

  it("does not persist the expanded nav when focusing search with /", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    localStorage.clear();
    act(() => {
      mounted!.container.querySelector(".nx-settings-view")!.dispatchEvent(
        new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }),
      );
    });
    await flush();
    expect(localStorage.getItem("nexterm.settingsNav.v1")).toBeNull();
  });

  it("renders an anchor for every indexed setting when the vault is password-protected and advanced panels are open", async () => {
    mocks.vaultStatus.mockResolvedValue({
      initialized: true,
      mode: "master",
      passwordless: false,
      unlocked: true,
      autoLockMinutes: 5,
    });
    mounted = withClient(createElement(SettingsView));
    await flush();
    for (const toggle of [...mounted.container.querySelectorAll("button")].filter(
      (b) => b.textContent?.trim() === "高级设置",
    )) {
      click(toggle);
    }
    await flush();
    const missing = SETTINGS_ITEMS.filter(
      (item) => !mounted!.container.querySelector(`[data-setting="${item.id}"]`),
    ).map((item) => item.id);
    expect(missing).toEqual([]);
  });
});
