/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  overview: vi.fn(),
  presets: vi.fn(),
  activate: vi.fn(),
  duplicate: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  modelApi: { overview: mocks.overview, presets: mocks.presets, activate: mocks.activate, duplicate: mocks.duplicate },
  aiApi: {},
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { ModelSelector } from "../../features/ai/ModelSelector";
import { ModelManager } from "../../features/ai/ModelPanel";
import { chatModelChoice, setChatModelChoice } from "../../features/ai/chatModelChoice";
import { useUi } from "../../app/store";

const profile = (id: string, name: string) => ({ id, name, baseUrl: `https://${id}.example/v1`, apiKey: "********", model: `${id}-model` });

let views: MountedView[] = [];

function chip(view: MountedView): HTMLButtonElement {
  return view.container.querySelector<HTMLButtonElement>("button.nx-chip")!;
}

function menuItem(view: MountedView, name: string): HTMLButtonElement {
  return [...view.container.querySelectorAll<HTMLButtonElement>("button.nx-menu-item")].find((b) =>
    b.querySelector(".nx-menu-label")?.textContent === name,
  )!;
}

beforeEach(() => {
  mocks.overview.mockResolvedValue({ profiles: [profile("a", "Work"), profile("b", "Fast")], activeId: "a" });
  mocks.presets.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast });
  setChatModelChoice("tab-1", null);
  setChatModelChoice("tab-2", null);
});

afterEach(() => {
  views.forEach((view) => view.unmount());
  views = [];
  vi.clearAllMocks();
});

describe("choosing a model in the AI panel", () => {
  it("changes only that chat tab and leaves the default alone", async () => {
    const first = mount(createElement(ModelSelector, { onManage: () => undefined, tabKey: "tab-1" }));
    const second = mount(createElement(ModelSelector, { onManage: () => undefined, tabKey: "tab-2" }));
    views.push(first, second);
    await flush();
    expect(chip(first).textContent).toContain("Work");

    click(chip(first));
    await flush();
    expect(menuItem(first, "Work").querySelector(".nx-menu-hint")?.textContent).toBe("默认");
    click(menuItem(first, "Fast"));
    await flush();

    expect(mocks.activate).not.toHaveBeenCalled();
    expect(chatModelChoice("tab-1")).toBe("b");
    expect(chip(first).textContent).toContain("Fast");
    expect(chip(second).textContent).toContain("Work");

    click(chip(first));
    await flush();
    click(menuItem(first, "Work"));
    await flush();
    expect(chatModelChoice("tab-1")).toBeUndefined();
  });

  it("follows the default again once the chosen profile is deleted", async () => {
    setChatModelChoice("tab-1", "b");
    mocks.overview.mockResolvedValue({ profiles: [profile("a", "Work")], activeId: "a" });
    const view = mount(createElement(ModelSelector, { onManage: () => undefined, tabKey: "tab-1" }));
    views.push(view);
    await flush();
    expect(chatModelChoice("tab-1")).toBeUndefined();
    expect(chip(view).textContent).toContain("Work");
  });
});

describe("copying a model profile", () => {
  it("copies the saved profile on the backend and opens the copy", async () => {
    mocks.duplicate.mockResolvedValue({ ...profile("c", "Work 副本") });
    const view = mount(createElement(ModelManager, {}));
    views.push(view);
    await flush();
    const copy = [...view.container.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "复制档案")!;
    expect(copy.disabled).toBe(false);
    click(copy);
    await flush();
    expect(mocks.duplicate).toHaveBeenCalledWith("a", "Work 副本");
    expect(view.container.textContent).toContain("Work 副本");
    expect(mocks.toast).toHaveBeenCalledWith("info", "已复制为「Work 副本」，修改后保存即可");
  });
});
