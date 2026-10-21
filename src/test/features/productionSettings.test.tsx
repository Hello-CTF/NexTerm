/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, deferred, flush, flushMacrotask, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn(),
  assetList: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async () => {
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    call: mocks.call,
    assetApi: { list: mocks.assetList },
    dbApi: {},
    vaultApi: {},
    sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: vi.fn().mockResolvedValue([]) },
  });
});

import { ProductionSettingsPanel } from "../../features/production/ProductionSettingsPanel";
import {
  classifyAssetProduction,
  isProductionEnvironment,
  resetProductionClassificationCache,
  useAssetProductionClassification,
  type AssetProductionClassification,
} from "../../features/production/productionSettings";
import { assetEnvironment } from "../../features/production/assetEnvironment";
import { notifyAssetDataChanged } from "../../ipc/assetEvents";
import { useUi } from "../../app/store";

const DEFAULT_DTO = {
  productionEnvironments: [] as string[],
  cronConnectOptIn: false,
  cronConnectProductionOptIn: false,
  broadcastConnectOptIn: false,
  broadcastConnectProductionOptIn: false,
  targetSetConnectOptIn: false,
  targetSetConnectProductionOptIn: false,
  aiTerminalConnectOptIn: false,
  aiTerminalConnectProductionOptIn: false,
};

let mounted: MountedView | undefined;

function settingsGet(input: unknown = DEFAULT_DTO) {
  let current = { ...DEFAULT_DTO, ...(input as object) };
  mocks.call.mockImplementation((cmd: string, args?: unknown) => {
    if (cmd === "production_settings_get") return Promise.resolve({ ...current });
    if (cmd === "production_settings_set") {
      current = { ...current, ...(args as object) };
      return Promise.resolve({ ...current });
    }
    return Promise.reject(new Error(`unexpected command ${cmd}`));
  });
}

// Clicks that start an async save: keep an act scope open across a macrotask
// so the save's state updates land inside act, never in a bare microtask
// checkpoint created by an early-returning flushUntil.
async function clickAndFlush(element: Element): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushMacrotask();
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast, accountEpoch: 0 });
  resetProductionClassificationCache();
  settingsGet();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetProductionClassificationCache();
});

describe("assetEnvironment", () => {
  it("reads canonical environment names from tags", () => {
    expect(assetEnvironment("production")).toBe("production");
    expect(assetEnvironment("prod")).toBe("production");
    expect(assetEnvironment("env:staging")).toBe("staging");
    expect(assetEnvironment("env:live")).toBe("live");
    expect(assetEnvironment("staging")).toBe("staging");
  });

  it("prefers production and otherwise the first candidate, like the backend", () => {
    expect(assetEnvironment("env:live, production")).toBe("production");
    expect(assetEnvironment("env:live, env:staging")).toBe("live");
    expect(assetEnvironment("web, env:dev")).toBe("development");
    expect(assetEnvironment("web, db")).toBe("");
  });

  it("splits full-width commas and trims with Go semantics", () => {
    expect(assetEnvironment("env：live，web")).toBe("");
    expect(assetEnvironment("env:live，web")).toBe("live");
    expect(assetEnvironment("\u0085env:live\u0085")).toBe("live");
  });
});

describe("isProductionEnvironment", () => {
  it("always matches the built-in names with Go fold semantics", () => {
    expect(isProductionEnvironment("production", [])).toBe(true);
    expect(isProductionEnvironment(" Production ", [])).toBe(true);
    expect(isProductionEnvironment("PROD", [])).toBe(true);
    expect(isProductionEnvironment("staging", [])).toBe(false);
    expect(isProductionEnvironment("", [])).toBe(false);
    expect(isProductionEnvironment("live", [])).toBe(false);
  });

  it("matches the configured per-owner additions", () => {
    expect(isProductionEnvironment("live", ["live"])).toBe(true);
    expect(isProductionEnvironment("LIVE", ["Live"])).toBe(true);
    expect(isProductionEnvironment("live", ["staging"])).toBe(false);
  });
});

function ClassificationProbe({ assetId }: { assetId: string }) {
  const classification: AssetProductionClassification | null = useAssetProductionClassification(assetId);
  return <span data-testid="classification-probe">{classification ? `${classification.environment}:${classification.production}` : "unclassified"}</span>;
}

function probeText(mountedView: MountedView): string {
  return mountedView.container.querySelector<HTMLElement>('[data-testid="classification-probe"]')?.textContent ?? "";
}

describe("production classification store", () => {
  it("classifies assets from tags with the configured list and resets on account switch", async () => {
    mocks.assetList.mockResolvedValue([
      { id: "a1", tags: "env:production" },
      { id: "a2", tags: "env:live" },
      { id: "a3", tags: "env:staging" },
    ]);
    settingsGet({ ...DEFAULT_DTO, productionEnvironments: ["live"] });
    mounted = mount(createElement(ClassificationProbe, { assetId: "a1" }));
    await flush();
    await flushUntil(() => mounted!.container.textContent === "production:true");

    expect(classifyAssetProduction("a1")).toEqual({ environment: "production", production: true });
    expect(classifyAssetProduction("a2")).toEqual({ environment: "live", production: true });
    expect(classifyAssetProduction("a3")).toEqual({ environment: "staging", production: false });
    expect(classifyAssetProduction("missing")).toBeNull();

    // The next owner has no production assets: the reset drops every
    // classification and the reload reflects the new owner.
    mocks.assetList.mockResolvedValue([]);
    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    expect(classifyAssetProduction("a1")).toBeNull();
    await flush();
    expect(mounted.container.textContent).toBe("unclassified");
    expect(classifyAssetProduction("a1")).toBeNull();
  });

  it("refreshes mounted consumers when asset tags change, without an account switch", async () => {
    mocks.assetList.mockResolvedValue([{ id: "a1", tags: "env:staging" }]);
    mounted = mount(createElement(ClassificationProbe, { assetId: "a1" }));
    await flush();
    await flushUntil(() => probeText(mounted!) === "staging:false");

    mocks.assetList.mockResolvedValue([{ id: "a1", tags: "env:production" }]);
    act(() => {
      notifyAssetDataChanged();
    });
    await flush();
    await flushUntil(() => probeText(mounted!) === "production:true");
    expect(classifyAssetProduction("a1")).toEqual({ environment: "production", production: true });
  });
});

describe("ProductionSettingsPanel", () => {
  it("loads the per-owner list and keeps every opt-in default off", async () => {
    settingsGet({ ...DEFAULT_DTO, productionEnvironments: ["live"] });
    mounted = mount(createElement(ProductionSettingsPanel));
    await flush();
    await flushUntil(() => mounted!.container.textContent?.includes("live") === true);

    expect(mocks.call).toHaveBeenCalledWith("production_settings_get");
    const toggles = [...mounted.container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
    expect(toggles).toHaveLength(8);
    expect(toggles.every((toggle) => !toggle.checked)).toBe(true);
  });

  it("saves an opt-in as an explicit patch and reflects the returned settings", async () => {
    mounted = mount(createElement(ProductionSettingsPanel));
    await flush();
    await flushUntil(() => mounted!.container.querySelector('input[aria-label="广播连接生产环境资产"]') !== null);

    const toggle = mounted.container.querySelector<HTMLInputElement>('input[aria-label="广播连接生产环境资产"]');
    if (!toggle) throw new Error("opt-in toggle not found");
    await clickAndFlush(toggle);
    await flushUntil(() => mocks.call.mock.calls.some(([cmd]) => cmd === "production_settings_set"));
    expect(mocks.call).toHaveBeenCalledWith("production_settings_set", { broadcastConnectProductionOptIn: true });
    await flushUntil(() => mounted!.container.querySelector<HTMLInputElement>('input[aria-label="广播连接生产环境资产"]')?.checked === true);
  });

  it("saves the AI terminal connect opt-ins as explicit patches", async () => {
    mounted = mount(createElement(ProductionSettingsPanel));
    await flush();
    await flushUntil(() => mounted!.container.querySelector('input[aria-label="AI 终端连接未连接资产"]') !== null);

    const toggle = mounted.container.querySelector<HTMLInputElement>('input[aria-label="AI 终端连接未连接资产"]');
    if (!toggle) throw new Error("AI terminal opt-in toggle not found");
    await clickAndFlush(toggle);
    await flushUntil(() => mocks.call.mock.calls.some(([cmd]) => cmd === "production_settings_set"));
    expect(mocks.call).toHaveBeenCalledWith("production_settings_set", { aiTerminalConnectOptIn: true });
    await flushUntil(() => mounted!.container.querySelector<HTMLInputElement>('input[aria-label="AI 终端连接未连接资产"]')?.checked === true);

    const productionToggle = mounted.container.querySelector<HTMLInputElement>('input[aria-label="AI 终端连接生产环境资产"]');
    if (!productionToggle) throw new Error("AI terminal production opt-in toggle not found");
    await clickAndFlush(productionToggle);
    expect(mocks.call).toHaveBeenCalledWith("production_settings_set", { aiTerminalConnectProductionOptIn: true });
  });

  it("adds and removes environment names and saves the whole list", async () => {
    mounted = mount(createElement(ProductionSettingsPanel));
    await flush();
    await flushUntil(() => mounted!.container.querySelector('input[aria-label="新增生产环境名"]') !== null);

    const input = mounted.container.querySelector<HTMLInputElement>('input[aria-label="新增生产环境名"]');
    if (!input) throw new Error("name input not found");
    setInputValue(input, "live");
    click(mounted.container.querySelector('button[aria-label="添加生产环境名"]')!);
    setInputValue(input, "canary");
    click(mounted.container.querySelector('button[aria-label="添加生产环境名"]')!);
    expect(mounted.container.textContent).toContain("canary");

    const saveButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.trim() === "保存列表",
    );
    if (!saveButton) throw new Error("save button not found");
    await clickAndFlush(saveButton);
    await flushUntil(() => mocks.call.mock.calls.some(([cmd]) => cmd === "production_settings_set"));
    expect(mocks.call).toHaveBeenCalledWith("production_settings_set", { productionEnvironments: ["live", "canary"] });
  });

  it("refreshes mounted consumers after the environment list is saved, without an account switch", async () => {
    mocks.assetList.mockResolvedValue([{ id: "a1", tags: "env:staging" }]);
    mounted = mount(createElement("div", null,
      createElement(ProductionSettingsPanel),
      createElement(ClassificationProbe, { assetId: "a1" }),
    ));
    await flush();
    await flushUntil(() => probeText(mounted!) === "staging:false");

    const input = mounted.container.querySelector<HTMLInputElement>('input[aria-label="新增生产环境名"]');
    if (!input) throw new Error("name input not found");
    setInputValue(input, "staging");
    click(mounted.container.querySelector('button[aria-label="添加生产环境名"]')!);
    const saveButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.trim() === "保存列表",
    );
    if (!saveButton) throw new Error("save button not found");
    await clickAndFlush(saveButton);
    await flushUntil(() => mocks.call.mock.calls.some(([cmd]) => cmd === "production_settings_set"));

    // The save invalidates and reloads the classification at the current
    // epoch: the mounted probe flips without any account switch.
    await flushUntil(() => probeText(mounted!) === "staging:true");
    expect(classifyAssetProduction("a1")).toEqual({ environment: "staging", production: true });
  });

  it("drops a stale get response after an account switch and shows the new owner", async () => {
    const first = deferred<typeof DEFAULT_DTO>();
    const second = deferred<typeof DEFAULT_DTO>();
    let gets = 0;
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "production_settings_get") {
        gets += 1;
        return gets === 1 ? first.promise : second.promise;
      }
      return Promise.reject(new Error(`unexpected command ${cmd}`));
    });

    mounted = mount(createElement(ProductionSettingsPanel));
    await flush();
    expect(mounted.container.textContent).toContain("正在加载");

    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    await flushUntil(() => gets === 2);

    // The previous owner's late response must not land in the new owner's view.
    await act(async () => {
      first.resolve({ ...DEFAULT_DTO, productionEnvironments: ["owner-a"] });
      await first.promise;
    });
    await flush();
    expect(mounted.container.textContent).not.toContain("owner-a");

    await act(async () => {
      second.resolve({ ...DEFAULT_DTO, productionEnvironments: ["owner-b"] });
      await second.promise;
    });
    await flushUntil(() => mounted!.container.textContent?.includes("owner-b") === true);
    expect(mounted.container.textContent).not.toContain("owner-a");
  });
});
