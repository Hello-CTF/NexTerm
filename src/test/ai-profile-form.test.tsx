/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
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

describe("ModelManager AI-1 profile fields", () => {
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

  it("按档案展示熔断生效值（自定义 3 次 / 60 秒）", async () => {
    await mountManager({ ...demoProfile(), circuitFailureThreshold: 3, circuitCooldownSeconds: 60 });

    expect(inputFor("熔断失败阈值").value).toBe("3");
    expect(inputFor("熔断冷却").value).toBe("60");
    expect(view!.container.textContent).toContain("连续失败 3 次");
    expect(view!.container.textContent).toContain("冷却 60 秒");
  });

  it("未配置熔断时展示后端默认值（5 次 / 300 秒）与占位符", async () => {
    await mountManager(demoProfile());

    expect(inputFor("熔断失败阈值").value).toBe("");
    expect(inputFor("熔断失败阈值").placeholder).toBe("默认 5");
    expect(inputFor("熔断冷却").value).toBe("");
    expect(inputFor("熔断冷却").placeholder).toBe("默认 300");
    expect(view!.container.textContent).toContain("连续失败 5 次");
    expect(view!.container.textContent).toContain("冷却 300 秒");
  });

  it("熔断提示明确与聊天里的重试分类标记区分", async () => {
    await mountManager(demoProfile());

    expect(view!.container.textContent).toContain("和聊天里的「可重试」标记是两回事");
  });

  it("编辑熔断阈值标记未保存并写回", async () => {
    await mountManager(demoProfile());

    setInputValue(inputFor("熔断失败阈值"), "7");
    expect(view!.container.textContent).toContain("有未保存的修改");
    clickButton(view!.container, "保存");
    await flush();

    expect(savedPayload()).toMatchObject({ id: "m-deepseek", circuitFailureThreshold: 7 });
  });

  it("新增档案以不限输出与空熔断配置开始", async () => {
    await mountManager(demoProfile());

    click(view!.container.querySelector('button[title="新增档案"]')!);
    await flush();

    expect(inputFor("最大输出").value).toBe("");
    expect(inputFor("熔断失败阈值").value).toBe("");
    expect(inputFor("熔断冷却").value).toBe("");

    clickButton(view!.container, "保存");
    await flush();

    const payload = savedPayload();
    expect(payload.maxTokens).toBeNull();
    expect(payload.circuitFailureThreshold).toBeNull();
    expect(payload.circuitCooldownSeconds).toBeNull();
  });
});
