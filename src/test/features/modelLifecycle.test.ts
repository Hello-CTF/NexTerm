/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { ModelProfile, ModelProfilesView } from "../../ipc/commands";
import type { ProviderConfigDto } from "../../ipc/types";
import {
  fallbackModelFromInput,
  fallbackModelLabel,
  idleTimeoutLabel,
  requestTimeoutLabel,
  sameModelProfile,
  selectModelProfileId,
  timeoutSecondsFromInput,
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
  test: vi.fn(),
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
    test: mocks.test,
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
    expect(sameModelProfile(withoutFallback, nullFallback)).toBe(true);
    expect(sameModelProfile(withFallback, nullFallback)).toBe(false);
    expect(sameModelProfile(withFallback, withoutFallback)).toBe(false);
    expect(sameModelProfile(withFallback, { ...withFallback, fallbackModel: "fb-2" })).toBe(false);
    expect(sameModelProfile(withFallback, { ...withFallback })).toBe(true);
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
    const label = [...view2!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.trim().startsWith("回退模型"),
    );
    const id = label?.htmlFor;
    const input = id ? view2!.container.querySelector(`input[id="${id}"]`) : null;
    if (!input) throw new Error("fallback input not found via its associated label");
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
    // 新档案参数全默认: 高级参数折叠,展开后才看得到回退模型字段
    clickButton(view2!.container, "高级参数");
    await flush();
    expect(fallbackInput().value).toBe("");
    clickButton(view2!.container, "保存");
    await flush();
    expect(mocks.save.mock.calls[0][0]).toMatchObject({ id: "", fallbackModel: null });
  });
});

describe("profile timeout semantics", () => {
  it("parses timeout input with per-field minimums", () => {
    expect(timeoutSecondsFromInput("", false)).toBeNull();
    expect(timeoutSecondsFromInput("  ", true)).toBeNull();
    expect(timeoutSecondsFromInput("5", false)).toBe(5);
    expect(timeoutSecondsFromInput("0", false)).toBeNull();
    expect(timeoutSecondsFromInput("0", true)).toBe(0);
    expect(timeoutSecondsFromInput("-2", true)).toBeNull();
    expect(timeoutSecondsFromInput("abc", false)).toBeNull();
  });

  it("treats absent and null timeouts as the same unset value", () => {
    const timed = (overrides: Partial<ModelProfile>): ModelProfile => ({
      ...profile("a"),
      ...overrides,
    });
    const withTimeouts = timed({ requestTimeoutSeconds: 5, idleTimeoutSeconds: 0 });
    expect(requestTimeoutLabel(withTimeouts)).toBe("5");
    expect(idleTimeoutLabel(withTimeouts)).toBe("0");
    expect(requestTimeoutLabel(profile("a"))).toBe("");
    expect(sameModelProfile(profile("a"), timed({ requestTimeoutSeconds: null }))).toBe(true);
    expect(sameModelProfile(withTimeouts, profile("a"))).toBe(false);
    expect(sameModelProfile(withTimeouts, timed({ requestTimeoutSeconds: 5, idleTimeoutSeconds: 0 }))).toBe(true);
    expect(sameModelProfile(withTimeouts, timed({ requestTimeoutSeconds: 6, idleTimeoutSeconds: 0 }))).toBe(false);
    expect(sameModelProfile(withTimeouts, timed({ requestTimeoutSeconds: 5, idleTimeoutSeconds: null }))).toBe(false);
  });
});

describe("ModelManager timeouts and connectivity testing", () => {
  let view3: MountedView | null = null;

  beforeEach(async () => {
    mocks.overview.mockResolvedValue({
      profiles: [{ ...profile("p1"), requestTimeoutSeconds: 300, idleTimeoutSeconds: 60 }],
      activeId: "p1",
    });
    mocks.presets.mockResolvedValue([]);
    mocks.save.mockImplementation(async (p: ModelProfile) => ({ ...p, id: p.id || "new-id" }));
    mocks.ask.mockResolvedValue(true);
    mocks.test.mockResolvedValue({ modelsOk: true, modelsError: null, chatOk: true, chatError: null });
    mocks.refresh.mockResolvedValue({ models: ["m1"], malformed: 0 });
    useUi.setState({ pushToast: mocks.toast });
    view3 = mount(createElement(ModelManager));
    await flush();
  });

  afterEach(() => {
    view3?.unmount();
    view3 = null;
  });

  function inputByLabel(prefix: string): HTMLInputElement {
    const label = [...view3!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.trim().startsWith(prefix),
    );
    const id = label?.htmlFor;
    const input = id ? view3!.container.querySelector(`input[id="${id}"]`) : null;
    if (!input) throw new Error(`input not found for label ${prefix}`);
    return input as HTMLInputElement;
  }

  function testButton(): HTMLButtonElement {
    const button = [...view3!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "测试连接",
    );
    if (!button) throw new Error("test connection button not found");
    return button as HTMLButtonElement;
  }

  it("shows saved timeout values and saves edits", async () => {
    expect(inputByLabel("stream/block 总超时").value).toBe("300");
    expect(inputByLabel("stream/block 空闲超时").value).toBe("60");
    setInputValue(inputByLabel("stream/block 总超时"), "30");
    setInputValue(inputByLabel("stream/block 空闲超时"), "0");
    expect(view3!.container.textContent).toContain("有未保存的修改");
    clickButton(view3!.container, "保存");
    await flush();
    expect(mocks.save).toHaveBeenCalledOnce();
    expect(mocks.save.mock.calls[0][0]).toMatchObject({
      id: "p1",
      requestTimeoutSeconds: 30,
      idleTimeoutSeconds: 0,
    });
  });

  it("tests the selected saved profile and renders the result", async () => {
    click(testButton());
    await flush();
    expect(mocks.test).toHaveBeenCalledOnce();
    expect(mocks.test).toHaveBeenCalledWith("p1");
    expect(view3!.container.textContent).toContain("连接正常");
  });

  it("renders honest failure details from the test result", async () => {
    mocks.test.mockResolvedValue({
      modelsOk: false,
      modelsError: "HTTP 401: bad key",
      chatOk: true,
      chatError: null,
    });
    click(testButton());
    await flush();
    expect(view3!.container.textContent).toContain("模型列表：HTTP 401: bad key");
  });

  it("disables the test button for unsaved or dirty drafts", async () => {
    expect(testButton().disabled).toBe(false);
    setInputValue(inputByLabel("stream/block 总超时"), "30");
    expect(testButton().disabled).toBe(true);
    click(view3!.container.querySelector('button[title="新增档案"]')!);
    await flush();
    expect(testButton().disabled).toBe(true);
  });

  it("surfaces malformed model entries from the refresh probe", async () => {
    mocks.refresh.mockResolvedValue({ models: ["m1"], malformed: 2 });
    clickButton(view3!.container, "刷新模型列表");
    await flush();
    expect(mocks.refresh).toHaveBeenCalledOnce();
    expect(mocks.toast).toHaveBeenCalledWith(
      "info",
      "端点返回的模型列表里有 2 个格式异常的条目，已跳过",
    );
  });
});
