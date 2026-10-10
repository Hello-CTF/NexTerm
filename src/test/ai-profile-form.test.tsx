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

  it("旧档案（demo 形状，无 maxTokens/熔断字段）显示为不限且保存保持不限", async () => {
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

  it("设置最大输出后标记未保存并写回数值", async () => {
    await mountManager(demoProfile());

    setInputValue(inputFor("最大输出"), "4096");
    expect(view!.container.textContent).toContain("有未保存的修改");
    clickButton(view!.container, "保存");
    await flush();

    expect(savedPayload()).toMatchObject({ id: "m-deepseek", maxTokens: 4096 });
  });

  it("清空最大输出 = 明确恢复不限", async () => {
    await mountManager({ ...demoProfile(), maxTokens: 4096 });
    expect(inputFor("最大输出").value).toBe("4096");

    setInputValue(inputFor("最大输出"), "");
    clickButton(view!.container, "保存");
    await flush();

    expect(savedPayload()).toMatchObject({ id: "m-deepseek", maxTokens: null });
  });

  it("非法最大输出输入按不限收敛，不会把垃圾写进档案", async () => {
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

  it("按档案展示熔断配置输入（自定义 3 次 / 60 秒）", async () => {
    await mountManager({ ...demoProfile(), circuitFailureThreshold: 3, circuitCooldownSeconds: 60 });

    expect(inputFor("自动暂停阈值").value).toBe("3");
    expect(inputFor("自动暂停时长").value).toBe("60");
  });

  it("未配置熔断时输入留空并展示后端默认值占位符", async () => {
    await mountManager(demoProfile());

    expect(inputFor("自动暂停阈值").value).toBe("");
    expect(inputFor("自动暂停阈值").placeholder).toBe("默认 5");
    expect(inputFor("自动暂停时长").value).toBe("");
    expect(inputFor("自动暂停时长").placeholder).toBe("默认 300");
  });

  it("编辑熔断阈值标记未保存并写回", async () => {
    await mountManager(demoProfile());

    setInputValue(inputFor("自动暂停阈值"), "7");
    expect(view!.container.textContent).toContain("有未保存的修改");
    clickButton(view!.container, "保存");
    await flush();

    expect(savedPayload()).toMatchObject({ id: "m-deepseek", circuitFailureThreshold: 7 });
  });

  it("新增档案以不限输出与空熔断配置开始，保存后才查熔断状态", async () => {
    await mountManager(demoProfile());
    const callsBefore = mocks.circuitStatus.mock.calls.length;

    click(view!.container.querySelector('button[title="新增档案"]')!);
    await flush();

    // 新档案参数全默认: 高级参数折叠,展开后才看得到字段
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

  it("保存后重新读取熔断状态", async () => {
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

  it("运行态为零值 DTO 时展示正常运行", async () => {
    mocks.circuitStatus.mockResolvedValue({ consecutiveFailures: 0, openUntil: null });
    await mountManager(demoProfile());

    expect(mocks.circuitStatus).toHaveBeenCalledWith("m-deepseek");
    expect(view!.container.textContent).toContain("运行正常：暂无连续失败");
    expect(view!.container.textContent).not.toContain("可重试");
  });

  it("熔断中展示连续失败数与冷却剩余时间", async () => {
    mocks.circuitStatus.mockResolvedValue({
      consecutiveFailures: 5,
      openUntil: Date.now() + 240_000,
    });
    await mountManager(demoProfile());

    expect(view!.container.textContent).toContain("已暂停：连续失败 5 次，240 秒后自动恢复");
  });

  it("冷却倒计时随时间递减", async () => {
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

  it("有连续失败但未熔断时展示闭合状态", async () => {
    mocks.circuitStatus.mockResolvedValue({ consecutiveFailures: 2, openUntil: null });
    await mountManager(demoProfile());

    expect(view!.container.textContent).toContain("运行中：最近连续失败 2 次");
  });

  it("读取失败时如实降级，点刷新可恢复", async () => {
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

  it("切换档案时按新档案重新加载运行态（demo open/closed 语义）", async () => {
    const glm: ModelProfile = {
      ...demoProfile(),
      id: "m-glm",
      name: "GLM 备用",
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

    expect(view!.container.textContent).toContain("已暂停：连续失败 5 次，300 秒后自动恢复");

    click(view!.container.querySelector('button[title="glm-4-plus"]')!);
    await flush();

    expect(mocks.circuitStatus).toHaveBeenLastCalledWith("m-glm");
    expect(view!.container.textContent).toContain("运行中：最近连续失败 1 次");
    expect(view!.container.textContent).not.toContain("已暂停");
  });
});

describe("ModelManager 快速填充预设与可选参数", () => {
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

  it("只渲染 4 个预设且 moonshot 排最前，不再拉取后端预设", async () => {
    await mountManager(demoProfile());

    const chips = [...view!.container.querySelectorAll("button")]
      .filter((b) => b.title?.startsWith("用 "))
      .map((b) => b.textContent?.trim());
    expect(chips).toEqual(["Moonshot", "DeepSeek", "Zhipu", "Ollama"]);
    expect(mocks.presets).not.toHaveBeenCalled();
  });

  it("新档案点预设复用后端地址、模型与上下文窗口，温度留空不传", async () => {
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

  it("编辑已有档案时预设不覆盖已填参数", async () => {
    await mountManager({ ...demoProfile(), temperature: 0.7, contextWindow: 64000 });

    clickButton(view!.container, "Moonshot");
    await flush();

    expect(inputFor("显示名称").value).toBe("DeepSeek 主力");
    expect(inputFor("Base URL").value).toBe("https://api.moonshot.cn/v1");
    expect(inputFor("模型名").value).toBe("kimi-k3");
    expect(inputFor("温度").value).toBe("0.7");
    expect(inputFor("上下文窗口").value).toBe("64000");
  });

  it("清空温度与上下文窗口后保存时省略这两个参数", async () => {
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

  it("档案 revision 刷新不覆盖未保存草稿，干净后按新数据更新", async () => {
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

  it("干净草稿会随档案 revision 更新", async () => {
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
