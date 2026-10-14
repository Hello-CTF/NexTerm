/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import type { ModelProfile } from "../ipc/commands";
import { click, clickButton, flush, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  overview: vi.fn(),
  save: vi.fn(),
  remove: vi.fn(),
  activate: vi.fn(),
  presets: vi.fn(),
  preset: vi.fn(),
  refresh: vi.fn(),
  test: vi.fn(),
  circuitStatus: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../ipc/commands", () => ({
  modelApi: {
    overview: mocks.overview,
    save: mocks.save,
    remove: mocks.remove,
    activate: mocks.activate,
    presets: mocks.presets,
    preset: mocks.preset,
    refresh: mocks.refresh,
    test: mocks.test,
    circuitStatus: mocks.circuitStatus,
  },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));

import { ModelManager } from "../features/ai/ModelPanel";
import { useUi } from "../app/store";

function demoProfile(): ModelProfile {
  return {
    id: "m-deepseek",
    name: "DeepSeek 主力",
    baseUrl: "https://api.deepseek.com/v1",
    apiKey: "sk-demo-0000000000000000000000000000",
    model: "deepseek-chat",
    temperature: 0.3,
    contextWindow: 64000,
    proxy: null,
    stream: true,
  };
}

let view: MountedView | null = null;

function inputFor(label: string): HTMLInputElement {
  const l = [...view!.container.querySelectorAll("label")].find((x) =>
    x.textContent?.trim().startsWith(label),
  );
  const id = l?.htmlFor;
  const input = id ? view!.container.querySelector(`input[id="${id}"]`) : null;
  if (!input) throw new Error(`input not found for label: ${label}`);
  return input as HTMLInputElement;
}

async function mountManager(profile: ModelProfile): Promise<void> {
  mocks.overview.mockResolvedValue({ profiles: [profile], activeId: profile.id });
  view = mount(createElement(ModelManager));
  await flush();
  clickButton(view!.container, "高级参数");
  await flush();
}

function savedPayload(): ModelProfile {
  expect(mocks.save).toHaveBeenCalledOnce();
  return mocks.save.mock.calls[0][0] as ModelProfile;
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

describe("ModelManager AI-1 profile fields", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.presets.mockResolvedValue([]);
    mocks.save.mockImplementation(async (p: ModelProfile) => ({ ...p, id: p.id || "new-id" }));
    mocks.ask.mockResolvedValue(true);
    mocks.circuitStatus.mockResolvedValue({ consecutiveFailures: 0, openUntil: null });
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    vi.useRealTimers();
  });

  it("legacy profiles (demo shape without maxTokens/circuit-breaker fields) display unlimited output and preserve it on save", async () => {
    await mountManager(demoProfile());

    expect(inputFor("最大输出").value).toBe("");

    const baseUrl = view!.container.querySelector(
      'input[placeholder="https://api.deepseek.com/v1"]',
    ) as HTMLInputElement;
    setInputValue(baseUrl, "https://new.example/v1");
    expect(view!.container.textContent).toContain("有未保存的修改");
    clickButton(view!.container, "保存");
    await flush();

    const payload = savedPayload();
    expect(payload).toMatchObject({ id: "m-deepseek", baseUrl: "https://new.example/v1" });
    expect(payload.maxTokens ?? null).toBeNull();
    expect(payload.circuitFailureThreshold ?? null).toBeNull();
    expect(payload.circuitCooldownSeconds ?? null).toBeNull();
  });

  it("setting maximum output marks the form dirty and saves the numeric value", async () => {
    await mountManager(demoProfile());

    setInputValue(inputFor("最大输出"), "4096");
    expect(view!.container.textContent).toContain("有未保存的修改");
    clickButton(view!.container, "保存");
    await flush();

    expect(savedPayload()).toMatchObject({ id: "m-deepseek", maxTokens: 4096 });
  });

  it("clearing maximum output explicitly restores unlimited output", async () => {
    await mountManager({ ...demoProfile(), maxTokens: 4096 });
    expect(inputFor("最大输出").value).toBe("4096");

    setInputValue(inputFor("最大输出"), "");
    clickButton(view!.container, "保存");
    await flush();

    expect(savedPayload()).toMatchObject({ id: "m-deepseek", maxTokens: null });
  });

  it("invalid maximum-output input falls back to unlimited instead of writing garbage into the profile", async () => {
    await mountManager(demoProfile());

    const input = inputFor("最大输出");
    setInputValue(input, "abc");
    expect(input.value).toBe("");
    setInputValue(input, "0");
    expect(input.value).toBe("");

    clickButton(view!.container, "保存");
    await flush();
    expect(savedPayload().maxTokens ?? null).toBeNull();
  });

  it("displays circuit-breaker settings from the profile (custom 3 failures / 60 seconds)", async () => {
    await mountManager({ ...demoProfile(), circuitFailureThreshold: 3, circuitCooldownSeconds: 60 });

    expect(inputFor("自动暂停阈值").value).toBe("3");
    expect(inputFor("自动暂停时长").value).toBe("60");
  });

  it("leaves circuit-breaker inputs empty when unconfigured and shows backend defaults as placeholders", async () => {
    await mountManager(demoProfile());

    expect(inputFor("自动暂停阈值").value).toBe("");
    expect(inputFor("自动暂停阈值").placeholder).toBe("默认 5");
    expect(inputFor("自动暂停时长").value).toBe("");
    expect(inputFor("自动暂停时长").placeholder).toBe("默认 300");
  });

  it("editing the circuit-breaker threshold marks the form dirty and saves the value", async () => {
    await mountManager(demoProfile());

    setInputValue(inputFor("自动暂停阈值"), "7");
    expect(view!.container.textContent).toContain("有未保存的修改");
    clickButton(view!.container, "保存");
    await flush();

    expect(savedPayload()).toMatchObject({ id: "m-deepseek", circuitFailureThreshold: 7 });
  });

  it("new profiles start with unlimited output and empty circuit-breaker settings; circuit status is queried only after save", async () => {
    await mountManager(demoProfile());
    const callsBefore = mocks.circuitStatus.mock.calls.length;

    click(view!.container.querySelector('button[title="新增档案"]')!);
    await flush();

    // New profile params all default: advanced params collapsed, fields only visible after expanding
    expect(() => inputFor("最大输出")).toThrow();
    clickButton(view!.container, "高级参数");
    await flush();

    expect(inputFor("最大输出").value).toBe("");
    expect(inputFor("自动暂停阈值").value).toBe("");
    expect(inputFor("自动暂停时长").value).toBe("");
    expect(view!.container.textContent).toContain("保存后可查看暂停状态");
    expect(mocks.circuitStatus.mock.calls.length).toBe(callsBefore);
    for (const [id] of mocks.circuitStatus.mock.calls) expect(id).not.toBe("");

    clickButton(view!.container, "保存");
    await flush();

    const payload = savedPayload();
    expect(payload.maxTokens).toBeNull();
    expect(payload.circuitFailureThreshold).toBeNull();
    expect(payload.circuitCooldownSeconds).toBeNull();
  });

  it("reloads circuit-breaker status after save", async () => {
    await mountManager(demoProfile());
    expect(mocks.circuitStatus).toHaveBeenCalledTimes(1);

    const baseUrl = view!.container.querySelector(
      'input[placeholder="https://api.deepseek.com/v1"]',
    ) as HTMLInputElement;
    setInputValue(baseUrl, "https://new.example/v1");
    clickButton(view!.container, "保存");
    await flush();

    expect(mocks.circuitStatus).toHaveBeenCalledTimes(2);
    expect(mocks.circuitStatus).toHaveBeenLastCalledWith("m-deepseek");
  });
});

describe("ModelManager circuit runtime status", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.presets.mockResolvedValue([]);
    mocks.save.mockImplementation(async (p: ModelProfile) => ({ ...p, id: p.id || "new-id" }));
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    vi.useRealTimers();
  });

  it("shows normal operation for a zero-valued runtime DTO", async () => {
    mocks.circuitStatus.mockResolvedValue({ consecutiveFailures: 0, openUntil: null });
    await mountManager(demoProfile());

    expect(mocks.circuitStatus).toHaveBeenCalledWith("m-deepseek");
    expect(view!.container.textContent).toContain("运行正常：暂无连续失败");
    expect(view!.container.textContent).not.toContain("可重试");
  });

  it("shows consecutive failures and remaining cooldown while the circuit is open", async () => {
    mocks.circuitStatus.mockResolvedValue({
      consecutiveFailures: 5,
      openUntil: Date.now() + 240_000,
    });
    await mountManager(demoProfile());

    expect(view!.container.textContent).toContain("已暂停：连续失败 5 次，240 秒后自动恢复");
  });

  it("decrements the cooldown countdown over time", async () => {
    vi.useFakeTimers();
    mocks.circuitStatus.mockResolvedValue({
      consecutiveFailures: 5,
      openUntil: Date.now() + 5000,
    });
    await mountManager(demoProfile());
    expect(view!.container.textContent).toContain("5 秒后自动恢复");

    await advance(2000);
    expect(view!.container.textContent).toContain("3 秒后自动恢复");

    await advance(3000);
    expect(view!.container.textContent).toContain("暂停已结束：连续失败 5 次，下次请求时恢复");
  });

  it("shows the closed state when there are consecutive failures but the circuit is not open", async () => {
    mocks.circuitStatus.mockResolvedValue({ consecutiveFailures: 2, openUntil: null });
    await mountManager(demoProfile());

    expect(view!.container.textContent).toContain("运行中：最近连续失败 2 次");
  });

  it("degrades accurately when status loading fails and recovers via refresh", async () => {
    mocks.circuitStatus.mockRejectedValue(new Error("boom"));
    await mountManager(demoProfile());

    expect(view!.container.textContent).toContain("暂停状态读取失败");
    expect(view!.container.textContent).toContain("boom");

    mocks.circuitStatus.mockResolvedValue({ consecutiveFailures: 1, openUntil: null });
    click(view!.container.querySelector('button[title="重新读取暂停状态"]')!);
    await flush();

    expect(mocks.circuitStatus).toHaveBeenCalledTimes(2);
    expect(view!.container.textContent).toContain("运行中：最近连续失败 1 次");
    expect(view!.container.textContent).not.toContain("暂停状态读取失败");
  });

  it("reloads runtime status for the newly selected profile (demo open/closed semantics)", async () => {
    const glm: ModelProfile = {
      ...demoProfile(),
      id: "m-glm",
      name: "GLM backup",
      model: "glm-4-plus",
    };
    mocks.overview.mockResolvedValue({
      profiles: [demoProfile(), glm],
      activeId: "m-deepseek",
    });
    mocks.circuitStatus.mockImplementation(async (id: string) => {
      if (id === "m-deepseek") return { consecutiveFailures: 5, openUntil: Date.now() + 300_000 };
      if (id === "m-glm") return { consecutiveFailures: 1, openUntil: null };
      return { consecutiveFailures: 0, openUntil: null };
    });
    view = mount(createElement(ModelManager));
    await flush();
    clickButton(view!.container, "高级参数");
    await flush();

    expect(view!.container.textContent).toContain("已暂停：连续失败 5 次，300 秒后自动恢复");

    click(view!.container.querySelector('button[title="glm-4-plus"]')!);
    await flush();
    clickButton(view!.container, "高级参数");
    await flush();

    expect(mocks.circuitStatus).toHaveBeenLastCalledWith("m-glm");
    expect(view!.container.textContent).toContain("运行中：最近连续失败 1 次");
    expect(view!.container.textContent).not.toContain("已暂停");
  });
});

describe("ModelManager quick-fill presets and optional parameters", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.save.mockImplementation(async (p: ModelProfile) => ({ ...p, id: p.id || "new-id" }));
    mocks.ask.mockResolvedValue(true);
    mocks.circuitStatus.mockResolvedValue({ consecutiveFailures: 0, openUntil: null });
    mocks.preset.mockImplementation(async (id: string) => ({
      ...demoProfile(),
      id: "",
      name: id,
      baseUrl:
        id === "zhipu"
          ? "https://open.bigmodel.cn/api/paas/v4"
          : id === "ollama"
            ? "http://127.0.0.1:11434/v1"
            : id === "moonshot"
              ? "https://api.moonshot.cn/v1"
              : "https://api.deepseek.com/v1",
      model:
        id === "zhipu"
          ? "glm-4.7-flash"
          : id === "ollama"
            ? "qwen2.5:7b"
            : id === "moonshot"
              ? "kimi-k3"
              : "deepseek-flash",
      contextWindow: id === "zhipu" ? 200_000 : id === "ollama" ? 32_000 : 1_000_000,
    }));
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    vi.useRealTimers();
  });

  it("renders only 4 presets with Moonshot first and no longer fetches backend presets", async () => {
    await mountManager(demoProfile());

    const chips = [...view!.container.querySelectorAll("button")]
      .filter((b) => b.title?.startsWith("用 "))
      .map((b) => b.textContent?.trim());
    expect(chips).toEqual(["Moonshot", "DeepSeek", "Zhipu", "Ollama"]);
    expect(mocks.presets).not.toHaveBeenCalled();
  });

  it("applying a preset to a new profile reuses the backend URL, model, and context window while leaving temperature unset", async () => {
    await mountManager(demoProfile());

    click(view!.container.querySelector('button[title="新增档案"]')!);
    await flush();
    clickButton(view!.container, "Zhipu");
    await flush();

    expect(mocks.preset).toHaveBeenCalledWith("zhipu");
    expect(inputFor("显示名称").value).toBe("zhipu");
    expect(inputFor("Base URL").value).toBe("https://open.bigmodel.cn/api/paas/v4");
    expect(inputFor("模型名").value).toBe("glm-4.7-flash");

    clickButton(view!.container, "高级参数");
    await flush();
    expect(inputFor("温度").value).toBe("");
    expect(inputFor("温度").placeholder).toBe("默认不传");
    expect(inputFor("上下文窗口").value).toBe("200000");
    expect(inputFor("上下文窗口").placeholder).toBe("默认不传");

    clickButton(view!.container, "保存");
    await flush();

    const payload = savedPayload();
    expect(payload.baseUrl).toBe("https://open.bigmodel.cn/api/paas/v4");
    expect(payload.model).toBe("glm-4.7-flash");
    expect((payload as Record<string, unknown>).temperature).toBeUndefined();
    expect(payload.contextWindow).toBe(200_000);
  });

  it("presets do not overwrite fields already filled in for existing profiles", async () => {
    await mountManager({ ...demoProfile(), temperature: 0.7, contextWindow: 64000 });

    clickButton(view!.container, "Moonshot");
    await flush();

    expect(inputFor("显示名称").value).toBe("DeepSeek 主力");
    expect(inputFor("Base URL").value).toBe("https://api.moonshot.cn/v1");
    expect(inputFor("模型名").value).toBe("kimi-k3");
    expect(inputFor("温度").value).toBe("0.7");
    expect(inputFor("上下文窗口").value).toBe("64000");
  });

  it("omits temperature and context window on save after clearing them", async () => {
    await mountManager({ ...demoProfile(), temperature: 0.7 });

    setInputValue(inputFor("温度"), "");
    setInputValue(inputFor("上下文窗口"), "");
    clickButton(view!.container, "保存");
    await flush();

    const payload = savedPayload();
    expect((payload as Record<string, unknown>).temperature).toBeUndefined();
    expect((payload as Record<string, unknown>).contextWindow).toBeUndefined();
    expect(payload.model).toBe("deepseek-chat");
  });

  it("profile revision refreshes do not overwrite unsaved drafts and apply new data once clean", async () => {
    const profile = demoProfile();
    await mountManager(profile);
    const baseUrl = view!.container.querySelector<HTMLInputElement>(
      'input[placeholder="https://api.deepseek.com/v1"]',
    )!;
    setInputValue(baseUrl, "https://draft.example/v1");

    mocks.overview.mockResolvedValue({
      profiles: [{ ...profile, baseUrl: "https://external.example/v1" }],
      activeId: profile.id,
    });
    act(() => useUi.getState().bumpModelProfilesRevision());
    await flush();
    expect(mocks.overview).toHaveBeenCalledTimes(2);
    expect(baseUrl.value).toBe("https://draft.example/v1");

  });

  it("clean drafts update with profile revisions", async () => {
    const profile = demoProfile();
    await mountManager(profile);
    const baseUrl = view!.container.querySelector<HTMLInputElement>(
      'input[placeholder="https://api.deepseek.com/v1"]',
    )!;
    mocks.overview.mockResolvedValue({
      profiles: [{ ...profile, baseUrl: "https://clean.example/v1" }],
      activeId: profile.id,
    });
    act(() => useUi.getState().bumpModelProfilesRevision());
    await flush();
    expect(mocks.overview).toHaveBeenCalledTimes(2);
    expect(baseUrl.value).toBe("https://clean.example/v1");
  });
});
