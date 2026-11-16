/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, mount, waitForRendered, type MountedView } from "./reactTestUtils";
import type { PrivacyStatus } from "../../ipc/privacy";

const mocks = vi.hoisted(() => ({ status: vi.fn(), addTab: vi.fn(), show: vi.fn(), overview: vi.fn(), toast: vi.fn() }));
vi.mock("../../ipc/commands", () => ({
  call: (command: string) => {
    if (command === "ai_privacy_status") return mocks.status();
    throw new Error(`Unexpected command: ${command}`);
  },
  modelApi: { overview: () => mocks.overview() },
}));
vi.mock("../../app/store", () => {
  const state = { addTab: mocks.addTab, pushToast: mocks.toast, modelProfilesRevision: 0 };
  const useUi = (selector: (s: typeof state) => unknown) => selector(state);
  useUi.getState = () => state;
  return { useUi };
});
vi.mock("../../features/settings/settingsNavigation", () => ({ showSettingsSection: mocks.show }));
import { PrivacyIndicator, privacyGuardFor, useChatPrivacy, usePrivacyGuard } from "../../features/ai/PrivacyIndicator";
import { chatPrivacyChoice, setChatPrivacyChoice } from "../../features/ai/chatPrivacyChoice";

const status = (patch: Partial<PrivacyStatus>): PrivacyStatus => ({
  config: { enabled: true, precision: "int8", revision: "a".repeat(40), version: "v0.1.3" },
  installed: [],
  runtime: { supported: true },
  selection: { available: true },
  diskUsage: 0,
  canManage: true,
  download: { running: false, precision: "int8", progress: { file: "", downloaded: 0, total: 0 } },
  ...patch,
});
function Probe() {
  const guard = usePrivacyGuard();
  return createElement("div", null, createElement(PrivacyIndicator, { guard }));
}
function ChatProbe() {
  const chat = useChatPrivacy("tab-1");
  return createElement("div", null, createElement(PrivacyIndicator, { guard: chat.guard, chat }));
}
let mounted: MountedView | undefined;
beforeEach(() => {
  vi.resetAllMocks();
  mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
  setChatPrivacyChoice("tab-1", null);
});
afterEach(() => { mounted?.unmount(); mounted = undefined; });

describe("AI chat privacy indicator", () => {
  it("shows the guard is active", async () => {
    mocks.status.mockResolvedValue(status({}));
    mounted = mount(createElement(Probe));
    await waitForRendered(() => expect(mounted!.container.querySelector('[aria-label="脱敏已开启"]')).not.toBeNull());
    // A lone shield: the wording is the tooltip, not text on the toolbar.
    const shield = mounted.container.querySelector<HTMLElement>('[aria-label="脱敏已开启"]')!;
    expect(shield.textContent).toBe("");
    expect(shield.title).toContain("脱敏已开启");
    expect(shield.querySelector("svg")).not.toBeNull();
  });

  it("links a broken guard to the privacy settings", async () => {
    mocks.status.mockResolvedValue(status({ selection: { available: false, error: "本地隐私模型文件缺失" } }));
    mounted = mount(createElement(Probe));
    await waitForRendered(() => expect(mounted!.container.querySelector('button[aria-label="脱敏不可用"]')).not.toBeNull());
    const link = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="脱敏不可用"]')!;
    expect(link.textContent).toBe("");
    click(link);
    expect(mocks.addTab).toHaveBeenCalledWith(expect.objectContaining({ kind: "settings" }));
    expect(mocks.show).toHaveBeenCalledWith("settings-ai-privacy");
  });

  it("stays hidden when the guard is off or its status cannot be read", async () => {
    mocks.status.mockRejectedValue(new Error("forbidden"));
    mounted = mount(createElement(Probe));
    await waitForRendered(() => expect(mocks.status).toHaveBeenCalled());
    expect(mounted.container.querySelector('[aria-label^="脱敏"]')).toBeNull();
  });
});

describe("model profile redaction override", () => {
  const off = (patch: Partial<PrivacyStatus> = {}) => status({ config: { enabled: false, precision: "int8", revision: "a".repeat(40), version: "v0.1.3" }, ...patch });

  it("follows the global switch without an override", () => {
    expect(privacyGuardFor(status({}))).toEqual({ enabled: true, broken: false });
    expect(privacyGuardFor(off())).toEqual({ enabled: false, broken: false });
  });

  it("turns the guard on or off for the profile whatever the global switch says", () => {
    expect(privacyGuardFor(off(), "on")).toEqual({ enabled: true, broken: false });
    expect(privacyGuardFor(status({}), "off")).toEqual({ enabled: false, broken: false });
  });

  it("reports a forced guard without a chosen local model as broken, since requests are blocked", () => {
    const unchosen = off({ config: { enabled: false, precision: "int8", revision: "", version: "" } });
    expect(privacyGuardFor(unchosen, "on")).toEqual({ enabled: true, broken: true });
  });
});

describe("chat redaction switch", () => {
  const off = (patch: Partial<PrivacyStatus> = {}) => status({ config: { enabled: false, precision: "int8", revision: "a".repeat(40), version: "v0.1.3" }, ...patch });
  const shield = () => mounted!.container.querySelector<HTMLButtonElement>("button.nx-privacy-icon")!;

  it("turns redaction off for the chat and back to following the global setting", async () => {
    mocks.status.mockResolvedValue(status({}));
    mounted = mount(createElement(ChatProbe));
    await waitForRendered(() => expect(shield()?.getAttribute("aria-pressed")).toBe("true"));
    click(shield());
    await waitForRendered(() => expect(shield().getAttribute("aria-pressed")).toBe("false"));
    expect(chatPrivacyChoice("tab-1")).toBe("off");
    expect(shield().title).toContain("本对话临时设置");
    click(shield());
    await waitForRendered(() => expect(shield().getAttribute("aria-pressed")).toBe("true"));
    // Back to what the global setting gives: the temporary choice is cleared, not pinned to "on".
    expect(chatPrivacyChoice("tab-1")).toBeUndefined();
  });

  it("turns redaction on for the chat while the global switch is off, and wins over the profile", async () => {
    mocks.status.mockResolvedValue(off());
    mocks.overview.mockResolvedValue({ profiles: [{ id: "p", name: "p", privacy: "off" }], activeId: "p" });
    mounted = mount(createElement(ChatProbe));
    await waitForRendered(() => expect(shield()?.getAttribute("aria-pressed")).toBe("false"));
    click(shield());
    await waitForRendered(() => expect(shield().getAttribute("aria-pressed")).toBe("true"));
    expect(chatPrivacyChoice("tab-1")).toBe("on");
  });

  it("sends the user to the settings instead of turning on redaction without a local model", async () => {
    mocks.status.mockResolvedValue(off({ config: { enabled: false, precision: "int8", revision: "", version: "" } }));
    mounted = mount(createElement(ChatProbe));
    await waitForRendered(() => expect(shield()).not.toBeNull());
    expect(shield().title).toContain("脱敏未配置");
    click(shield());
    expect(chatPrivacyChoice("tab-1")).toBeUndefined();
    expect(mocks.show).toHaveBeenCalledWith("settings-ai-privacy");
  });

  it("lets a broken guard be turned off for the chat", async () => {
    mocks.status.mockResolvedValue(status({ selection: { available: false, error: "missing" } }));
    mounted = mount(createElement(ChatProbe));
    await waitForRendered(() => expect(mounted!.container.querySelector('button[aria-label="脱敏不可用"]')).not.toBeNull());
    click(shield());
    expect(chatPrivacyChoice("tab-1")).toBe("off");
  });
});
