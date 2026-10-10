/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { ModelProfile } from "../../ipc/commands";
import {
  MODEL_PARAM_DEFAULTS,
  modelParamsAtDefaults,
  resetModelParams,
} from "../../features/ai/modelLifecycle";
import { clickButton, flush, mount, type MountedView } from "./reactTestUtils";

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

const SAVED: ModelProfile = {
  id: "p1",
  name: "公司 DeepSeek",
  baseUrl: "https://api.deepseek.com/v1",
  apiKey: "sk-secret-1",
  model: "deepseek-chat",
  temperature: 1.2,
  contextWindow: 8192,
  proxy: "http://127.0.0.1:7890",
  stream: false,
  fallbackModel: "fb-1",
  reasoningEffort: "high",
  requestTimeoutSeconds: 30,
  idleTimeoutSeconds: 45,
  maxTokens: 2048,
  circuitFailureThreshold: 2,
  circuitCooldownSeconds: 90,
};

let view: MountedView | null = null;

function inputFor(label: string): HTMLInputElement {
  const mounted = view?.container;
  const l = [...(mounted?.querySelectorAll("label") ?? [])].find((x) =>
    x.textContent?.trim().startsWith(label),
  );
  const id = l?.htmlFor;
  const input = id ? mounted?.querySelector(`input[id="${id}"]`) : null;
  if (!input) throw new Error(`input not found for label: ${label}`);
  return input as HTMLInputElement;
}

async function mountManager(profile: ModelProfile): Promise<void> {
  mocks.overview.mockResolvedValue({ profiles: [profile], activeId: profile.id });
  view = mount(createElement(ModelManager));
  await flush();
}

describe("resetModelParams / modelParamsAtDefaults", () => {
  it("只重置参数字段，保留 id、名称、地址、密钥与模型名", () => {
    const reset = resetModelParams(SAVED);
    expect(reset).toEqual({ ...SAVED, ...MODEL_PARAM_DEFAULTS });
    expect(reset.id).toBe("p1");
    expect(reset.apiKey).toBe("sk-secret-1");
    expect(reset.baseUrl).toBe("https://api.deepseek.com/v1");
    expect(reset.model).toBe("deepseek-chat");
    expect(reset.name).toBe("公司 DeepSeek");
  });

  it("识别参数是否处于默认", () => {
    expect(modelParamsAtDefaults(resetModelParams(SAVED))).toBe(true);
    expect(modelParamsAtDefaults(SAVED)).toBe(false);
    expect(modelParamsAtDefaults({ ...resetModelParams(SAVED), stream: false })).toBe(false);
    expect(
      modelParamsAtDefaults({ ...resetModelParams(SAVED), fallbackModel: "fb-2" }),
    ).toBe(false);
  });
});

describe("ModelManager 恢复默认参数", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    mocks.presets.mockResolvedValue([]);
    mocks.save.mockImplementation(async (p: ModelProfile) => ({ ...p, id: p.id || "new-id" }));
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("重置参数但保留密钥与身份字段，且不触发保存", async () => {
    await mountManager(SAVED);

    clickButton(view!.container, "恢复默认参数");
    await flush();

    expect(inputFor("温度").value).toBe("");
    expect(inputFor("上下文窗口").value).toBe("");
    expect(inputFor("代理").value).toBe("");
    expect(inputFor("回退模型").value).toBe("");
    expect(inputFor("思考强度").value).toBe("");
    expect(inputFor("最大输出").value).toBe("");
    expect(inputFor("stream/block 总超时").value).toBe("");
    expect(inputFor("stream/block 空闲超时").value).toBe("");
    expect(inputFor("自动暂停阈值").value).toBe("");
    expect(inputFor("自动暂停时长").value).toBe("");
    expect(view!.container.textContent).toContain("端点默认");
    expect(view!.container.textContent).toContain("stream/block 总超时");
    expect(view!.container.textContent).toContain("stream/block 空闲超时");
    const stream = view!.container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    expect(stream.checked).toBe(true);

    expect(inputFor("展示名").value).toBe("公司 DeepSeek");
    expect(inputFor("Base URL").value).toBe("https://api.deepseek.com/v1");
    expect(inputFor("API Key").value).toBe("sk-secret-1");
    expect(inputFor("模型名").value).toBe("deepseek-chat");

    expect(mocks.save).not.toHaveBeenCalled();
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(mocks.activate).not.toHaveBeenCalled();
    expect(view!.container.textContent).toContain("有未保存的修改");
  });

  it("重置后点保存把清空结果写入档案，温度与上下文窗口不传", async () => {
    await mountManager(SAVED);

    clickButton(view!.container, "恢复默认参数");
    await flush();
    clickButton(view!.container, "保存");
    await flush();

    expect(mocks.save).toHaveBeenCalledTimes(1);
    const expected: Record<string, unknown> = { ...SAVED, ...MODEL_PARAM_DEFAULTS };
    delete expected.temperature;
    delete expected.contextWindow;
    delete expected.reasoningEffort;
    expect(mocks.save.mock.calls[0][0]).toEqual(expected);
  });

  it("参数已在默认时按钮禁用", async () => {
    await mountManager({ ...SAVED, ...MODEL_PARAM_DEFAULTS, fallbackModel: null });

    const reset = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "恢复默认参数",
    ) as HTMLButtonElement;
    expect(reset.disabled).toBe(true);
  });
});

describe("ModelManager 高级参数折叠", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    mocks.presets.mockResolvedValue([]);
    mocks.save.mockImplementation(async (p: ModelProfile) => ({ ...p, id: p.id || "new-id" }));
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("参数全默认时高级参数折叠,点击展开后才看得到字段", async () => {
    await mountManager({ ...SAVED, ...MODEL_PARAM_DEFAULTS, fallbackModel: null });

    const toggle = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "高级参数",
    ) as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(() => inputFor("温度")).toThrow();

    clickButton(view!.container, "高级参数");
    await flush();

    expect(inputFor("温度").value).toBe("0.3");
    expect(inputFor("上下文窗口").value).toBe("32768");
    expect(inputFor("回退模型").value).toBe("");
  });

  it("任一高级参数非默认时自动展开", async () => {
    await mountManager(SAVED);

    const toggle = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "高级参数",
    ) as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(inputFor("温度").value).toBe("1.2");
    expect(inputFor("回退模型").value).toBe("fb-1");
  });

  it("最大输出/超时/熔断任一非空也自动展开", async () => {
    await mountManager({
      ...SAVED,
      ...MODEL_PARAM_DEFAULTS,
      fallbackModel: null,
      maxTokens: 4096,
    });

    expect(inputFor("最大输出").value).toBe("4096");
  });

  it("切换到参数全默认的档案后回到折叠", async () => {
    const plain: ModelProfile = {
      ...SAVED,
      id: "p2",
      name: "默认档案",
      ...MODEL_PARAM_DEFAULTS,
      fallbackModel: null,
    };
    mocks.overview.mockResolvedValue({ profiles: [SAVED, plain], activeId: SAVED.id });
    view = mount(createElement(ModelManager));
    await flush();

    expect(inputFor("温度").value).toBe("1.2");

    clickButton(view!.container, "默认档案");
    await flush();

    const toggle = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "高级参数",
    ) as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(() => inputFor("温度")).toThrow();
  });
});
