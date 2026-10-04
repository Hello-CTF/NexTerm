/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { ModelProfile, ModelProfilesView } from "../../ipc/commands";
import { clickButton, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

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

const MASKED_KEY = "••••••••••••";

function savedProfile(): ModelProfile {
  return {
    id: "p1",
    name: "公司 DeepSeek",
    baseUrl: "https://ai.example/v1",
    apiKey: MASKED_KEY,
    model: "deepseek-chat",
    temperature: 0.3,
    contextWindow: 32768,
    proxy: null,
    stream: true,
    fallbackModel: null,
  };
}

describe("ModelManager masked key boundary", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    const overview: ModelProfilesView = { profiles: [savedProfile()], activeId: "p1" };
    mocks.overview.mockResolvedValue(overview);
    mocks.presets.mockResolvedValue([]);
    mocks.refresh.mockResolvedValue(["deepseek-chat", "deepseek-reasoner"]);
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
    view = mount(createElement(ModelManager));
    await flush();
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    vi.clearAllMocks();
  });

  it("sends the saved masked key to the backend for refresh without local resolution", async () => {
    clickButton(view!.container, "刷新模型列表");
    await flush();
    expect(mocks.refresh).toHaveBeenCalledOnce();
    expect(mocks.refresh.mock.calls[0][0]).toMatchObject({
      id: "p1",
      baseUrl: "https://ai.example/v1",
      apiKey: MASKED_KEY,
    });
    expect(view!.container.textContent).toContain("deepseek-reasoner");
  });

  it("sends an explicitly entered plaintext key as-is on refresh", async () => {
    const keyInput = view!.container.querySelector(
      'input[placeholder="sk-..."]',
    ) as HTMLInputElement;
    setInputValue(keyInput, "sk-new-explicit");
    clickButton(view!.container, "刷新模型列表");
    await flush();
    expect(mocks.refresh).toHaveBeenCalledOnce();
    expect(mocks.refresh.mock.calls[0][0]).toMatchObject({
      id: "p1",
      apiKey: "sk-new-explicit",
    });
  });

  it("surfaces backend refresh failures as honest errors", async () => {
    mocks.refresh.mockRejectedValue(new Error("AI model profile not found: p1"));
    clickButton(view!.container, "刷新模型列表");
    await flush();
    expect(mocks.refresh).toHaveBeenCalledOnce();
    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("拉取模型列表失败"),
    );
    expect(view!.container.textContent).not.toContain("deepseek-reasoner");
  });
});
