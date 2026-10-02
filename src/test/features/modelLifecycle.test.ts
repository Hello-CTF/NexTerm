/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { ModelProfile, ModelProfilesView } from "../../ipc/commands";
import type { ProviderConfigDto } from "../../ipc/types";
import {
  fallbackModelFromInput,
  fallbackModelLabel,
  sameModelProfile,
  selectModelProfileId,
} from "../../features/ai/modelLifecycle";
import { click, clickButton, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  overview: vi.fn(),
  save: vi.fn(),
  remove: vi.fn(),
  activate: vi.fn(),
  presets: vi.fn(),
  preset: vi.fn(),
  refresh: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  modelApi: {
    overview: mocks.overview,
    save: mocks.save,
    remove: mocks.remove,
    activate: mocks.activate,
    presets: mocks.presets,
    preset: mocks.preset,
    refresh: mocks.refresh,
  },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { ModelManager } from "../../features/ai/ModelPanel";
import { useUi } from "../../app/store";

function profile(id: string): ModelProfile {
  return {
    id,
    name: id,
    baseUrl: "https://example.test",
    apiKey: "key",
    model: "model",
    temperature: 0.3,
    contextWindow: 32768,
    proxy: null,
    stream: true,
  };
}

function view(activeId: string | null, ids = ["a", "b"]): ModelProfilesView {
  return { activeId, profiles: ids.map(profile) };
}

describe("model profile selection", () => {
  it("keeps a valid preferred profile", () => {
    expect(selectModelProfileId(view("a"), "b")).toBe("b");
  });

  it("falls back from a deleted preferred id to active, then to the first profile", () => {
    expect(selectModelProfileId(view("b"), "deleted")).toBe("b");
    expect(selectModelProfileId(view("deleted"), "also-deleted")).toBe("a");
    expect(selectModelProfileId(view(null), "deleted")).toBe("a");
  });

  it("returns null for an empty profile list", () => {
    expect(selectModelProfileId(view(null, []))).toBeNull();
  });
});

describe("fallbackModel DTO and pure editor semantics", () => {
  it("normalizes input: non-empty sets, empty explicitly clears", () => {
    expect(fallbackModelFromInput(" gpt-4o-mini ")).toBe("gpt-4o-mini");
    expect(fallbackModelFromInput("")).toBeNull();
    expect(fallbackModelFromInput("   ")).toBeNull();
  });

  it("treats absent and null as the same unset value for display and dirty checks", () => {
    const withFallback = { ...profile("a"), fallbackModel: "fb-1" };
    const withoutFallback = profile("a");
    const nullFallback = { ...profile("a"), fallbackModel: null };
    expect(fallbackModelLabel(withFallback)).toBe("fb-1");
    expect(fallbackModelLabel(withoutFallback)).toBe("");
    expect(fallbackModelLabel(nullFallback)).toBe("");
    // undefined 与 null 都是「未设置」⇒ 不算改动；设置/清除/改值 ⇒ 算改动。
    expect(sameModelProfile(withoutFallback, nullFallback)).toBe(true);
    expect(sameModelProfile(withFallback, nullFallback)).toBe(false);
    expect(sameModelProfile(withFallback, withoutFallback)).toBe(false);
    expect(sameModelProfile(withFallback, { ...withFallback, fallbackModel: "fb-2" })).toBe(false);
    expect(sameModelProfile(withFallback, { ...withFallback })).toBe(true);
    // 其它字段的比较没有被回退字段破坏。
    expect(sameModelProfile(withFallback, { ...withFallback, baseUrl: "https://other" })).toBe(false);
  });

  it("keeps fallbackModel optional on both DTOs (no invented companion fields)", () => {
    const dto: ProviderConfigDto = {
      baseUrl: "https://example.test",
      apiKey: "key",
      model: "model",
      temperature: 0.3,
      contextWindow: 32768,
      proxy: null,
      stream: true,
    };
    expect("fallbackModel" in dto).toBe(false);
    const withClear: ProviderConfigDto = { ...dto, fallbackModel: null };
    expect(withClear.fallbackModel).toBeNull();
    const withSet: ProviderConfigDto = { ...dto, fallbackModel: "fb-1" };
    expect(withSet.fallbackModel).toBe("fb-1");
    const prof: ModelProfile = { ...profile("a"), fallbackModel: "fb-1" };
    expect(prof.fallbackModel).toBe("fb-1");
  });
});

describe("ModelManager fallback editing", () => {
  let view2: MountedView | null = null;

  beforeEach(async () => {
    mocks.overview.mockResolvedValue({
      profiles: [{ ...profile("p1"), fallbackModel: "fb-1" }],
      activeId: "p1",
    });
    mocks.presets.mockResolvedValue([]);
    mocks.save.mockImplementation(async (p: ModelProfile) => ({ ...p, id: p.id || "new-id" }));
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
    view2 = mount(createElement(ModelManager));
    await flush();
  });

  afterEach(() => {
    view2?.unmount();
    view2 = null;
  });

  function fallbackInput(): HTMLInputElement {
    const input = view2!.container.querySelector('input[aria-label="回退模型"]');
    if (!input) throw new Error("fallback input not found");
    return input as HTMLInputElement;
  }

  it("preserves an existing fallback when saving unrelated fields", async () => {
    expect(fallbackInput().value).toBe("fb-1");
    const baseUrl = view2!.container.querySelector(
      'input[placeholder="https://api.deepseek.com/v1"]',
    ) as HTMLInputElement;
    setInputValue(baseUrl, "https://new.example/v1");
    clickButton(view2!.container, "保存");
    await flush();
    expect(mocks.save).toHaveBeenCalledOnce();
    expect(mocks.save.mock.calls[0][0]).toMatchObject({
      id: "p1",
      baseUrl: "https://new.example/v1",
      fallbackModel: "fb-1",
    });
  });

  it("clears the fallback only when the field is explicitly emptied", async () => {
    setInputValue(fallbackInput(), "");
    clickButton(view2!.container, "保存");
    await flush();
    expect(mocks.save.mock.calls[0][0]).toMatchObject({ id: "p1", fallbackModel: null });
  });

  it("marks a fallback-only edit as unsaved and saves the new value", async () => {
    expect(view2!.container.textContent).not.toContain("有未保存的修改");
    setInputValue(fallbackInput(), "fb-2");
    expect(view2!.container.textContent).toContain("有未保存的修改");
    clickButton(view2!.container, "保存");
    await flush();
    expect(mocks.save.mock.calls[0][0]).toMatchObject({ id: "p1", fallbackModel: "fb-2" });
  });

  it("starts a brand-new profile with an explicitly empty fallback", async () => {
    click(view2!.container.querySelector('button[title="新增档案"]')!);
    await flush();
    expect(fallbackInput().value).toBe("");
    clickButton(view2!.container, "保存");
    await flush();
    expect(mocks.save.mock.calls[0][0]).toMatchObject({ id: "", fallbackModel: null });
  });
});
