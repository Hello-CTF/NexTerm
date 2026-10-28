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
  it("resets only parameter fields and preserves id, name, address, key, and model", () => {
    const reset = resetModelParams(SAVED);
    expect(reset).toEqual({ ...SAVED, ...MODEL_PARAM_DEFAULTS });
    expect(reset.id).toBe("p1");
    expect(reset.apiKey).toBe("sk-secret-1");
    expect(reset.baseUrl).toBe("https://api.deepseek.com/v1");
    expect(reset.model).toBe("deepseek-chat");
    expect(reset.name).toBe("公司 DeepSeek");
  });

  it("detects whether parameters are at their defaults", () => {
    expect(modelParamsAtDefaults(resetModelParams(SAVED))).toBe(true);
    expect(modelParamsAtDefaults(SAVED)).toBe(false);
    expect(modelParamsAtDefaults({ ...resetModelParams(SAVED), stream: false })).toBe(false);
    expect(
      modelParamsAtDefaults({ ...resetModelParams(SAVED), fallbackModel: "fb-2" }),
    ).toBe(false);
  });
});

describe("ModelManager reset parameters to defaults", () => {
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

  it("resets parameters without saving while preserving the key and identity fields", async () => {
    await mountManager(SAVED);

    clickButton(view!.container, "高级参数");
    await flush();
    clickButton(view!.container, "恢复默认参数");
    await flush();

    expect(inputFor("温度").value).toBe("");
    expect(inputFor("上下文窗口").value).toBe("");
    expect(inputFor("代理").value).toBe("");
    expect(inputFor("回退模型").value).toBe("");
    expect(inputFor("思考强度").value).toBe("");
    expect(inputFor("最大输出").value).toBe("");
    expect(inputFor("请求超时").value).toBe("");
    expect(inputFor("响应空闲超时").value).toBe("");
    expect(inputFor("自动暂停阈值").value).toBe("");
    expect(inputFor("自动暂停时长").value).toBe("");
    expect(inputFor("思考强度").placeholder).toContain("接口地址默认");
    expect(view!.container.textContent).toContain("请求超时");
    expect(view!.container.textContent).toContain("响应空闲超时");
    const stream = view!.container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    expect(stream.checked).toBe(true);

    expect(inputFor("显示名称").value).toBe("公司 DeepSeek");
    expect(inputFor("Base URL").value).toBe("https://api.deepseek.com/v1");
    expect(inputFor("API Key").value).toBe("sk-secret-1");
    expect(inputFor("模型名").value).toBe("deepseek-chat");

    expect(mocks.save).not.toHaveBeenCalled();
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(mocks.activate).not.toHaveBeenCalled();
    expect(view!.container.textContent).toContain("有未保存的修改");
  });

  it("saves cleared parameters after reset and omits temperature and context window", async () => {
    await mountManager(SAVED);

    clickButton(view!.container, "高级参数");
    await flush();
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

  it("disables the reset button when parameters are already at their defaults", async () => {
    await mountManager({ ...SAVED, ...MODEL_PARAM_DEFAULTS, fallbackModel: null });

    const reset = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "恢复默认参数",
    ) as HTMLButtonElement;
    expect(reset.disabled).toBe(true);
  });
});

describe("ModelManager advanced parameters collapsed", () => {
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

  it("collapses advanced parameters when all values are at their defaults and reveals fields only after expanding", async () => {
    await mountManager({ ...SAVED, ...MODEL_PARAM_DEFAULTS, fallbackModel: null });

    const toggle = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "高级参数",
    ) as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(() => inputFor("温度")).toThrow();

    clickButton(view!.container, "高级参数");
    await flush();

    expect(inputFor("温度").value).toBe("0.3");
    expect(inputFor("上下文窗口").value).toBe("262144");
    expect(inputFor("回退模型").value).toBe("");
  });

  it("also collapses advanced parameters by default when any value differs from its default and reveals it after manual expansion", async () => {
    await mountManager(SAVED);

    const toggle = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "高级参数",
    ) as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(() => inputFor("温度")).toThrow();

    clickButton(view!.container, "高级参数");
    await flush();

    expect(inputFor("温度").value).toBe("1.2");
    expect(inputFor("回退模型").value).toBe("fb-1");
  });

  it("also collapses advanced parameters by default when max output, timeouts, or circuit breaking is set and reveals them after manual expansion", async () => {
    await mountManager({
      ...SAVED,
      ...MODEL_PARAM_DEFAULTS,
      fallbackModel: null,
      maxTokens: 4096,
    });

    const toggle = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "高级参数",
    ) as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(() => inputFor("最大输出")).toThrow();

    clickButton(view!.container, "高级参数");
    await flush();

    expect(inputFor("最大输出").value).toBe("4096");
  });

  it("collapses advanced parameters again after switching profiles", async () => {
    const plain: ModelProfile = {
      ...SAVED,
      id: "p2",
      name: "Default profile",
      ...MODEL_PARAM_DEFAULTS,
      fallbackModel: null,
    };
    mocks.overview.mockResolvedValue({ profiles: [SAVED, plain], activeId: SAVED.id });
    view = mount(createElement(ModelManager));
    await flush();

    clickButton(view!.container, "高级参数");
    await flush();
    expect(inputFor("温度").value).toBe("1.2");

    clickButton(view!.container, "Default profile");
    await flush();

    const toggle = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "高级参数",
    ) as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(() => inputFor("温度")).toThrow();
  });
});
