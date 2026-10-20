/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, deferred, flush, mount, setInputValue, waitFor, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  forwardEnv: vi.fn(),
  forwardList: vi.fn(),
  forwardCreate: vi.fn(),
  forwardCreateSocks: vi.fn(),
  forwardCreateRemote: vi.fn(),
  forwardRemove: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    forwardApi: {
      env: mocks.forwardEnv,
      list: mocks.forwardList,
      create: mocks.forwardCreate,
      createSocks: mocks.forwardCreateSocks,
      createRemote: mocks.forwardCreateRemote,
      remove: mocks.forwardRemove,
    },
  };
});
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));

import { ForwardPanel } from "../features/forward/ForwardPanel";
import { ForwardSettingsPanel } from "../features/forward/ForwardSettingsPanel";
import {
  ForwardSettingsApiProvider,
  type ForwardRecoverySettings,
  type ForwardRuleSettings,
  type ForwardSettingsApi,
} from "../features/forward/forwardSettingsApi";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  document.body.replaceChildren();
  mocks.forwardEnv.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
  mocks.forwardList.mockResolvedValue([]);
  mocks.forwardCreateRemote.mockResolvedValue({ id: "f1", kind: "remote" });
  mocks.forwardRemove.mockResolvedValue(null);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

function mountForward(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(QueryClientProvider, { client }, createElement(ForwardPanel, { sessionId: "s" })),
  );
}

function blur(element: HTMLElement): void {
  act(() => {
    element.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
  });
}

function fieldError(input: HTMLInputElement): HTMLElement | null {
  const id = input.getAttribute("aria-describedby");
  if (!id) return null;
  return document.getElementById(id);
}

function inputBy(selector: string): HTMLInputElement {
  const input = mounted!.container.querySelector<HTMLInputElement>(selector);
  if (!input) throw new Error(`input not found: ${selector}`);
  return input;
}

function switchToRemote(): void {
  clickButton(mounted!.container, "远程转发");
}

describe("ForwardPanel remote forwarding (-R)", () => {
  it("renders remote listen address and target fields after switching to remote forwarding, with port 18080 by default", async () => {
    mountForward();
    await flush();
    expect(mounted!.container.querySelector('input[aria-label="远端监听地址"]')).toBeNull();
    switchToRemote();
    const bindHost = inputBy('input[aria-label="远端监听地址"]');
    expect(bindHost.value).toBe("127.0.0.1");
    expect(inputBy('input[placeholder="18080"]')).toBeTruthy();
    expect(inputBy('input[aria-label="目标主机"]')).toBeTruthy();
    expect(inputBy('input[aria-label="目标端口"]')).toBeTruthy();
  });

  it("shows an inline error and focuses the listen address when it is empty on submit, without creating", async () => {
    mountForward();
    await flush();
    switchToRemote();
    const bindHost = inputBy('input[aria-label="远端监听地址"]');
    setInputValue(bindHost, " ");
    clickButton(mounted!.container, "创建");
    expect(bindHost.getAttribute("aria-invalid")).toBe("true");
    const error = fieldError(bindHost);
    expect(error?.getAttribute("role")).toBe("alert");
    expect(error?.textContent).toBe("远端监听地址不能为空");
    expect(document.activeElement).toBe(bindHost);
    expect(mocks.forwardCreateRemote).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("shows separate inline errors for invalid remote and target ports on submit", async () => {
    mountForward();
    await flush();
    switchToRemote();
    const bindPort = inputBy('input[placeholder="18080"]');
    const targetPort = inputBy('input[aria-label="目标端口"]');
    setInputValue(bindPort, "70000");
    setInputValue(targetPort, "0");
    clickButton(mounted!.container, "创建");
    expect(fieldError(bindPort)?.textContent).toBe("远端端口要填 1–65535 之间的整数");
    expect(fieldError(targetPort)?.textContent).toBe("目标端口要填 1–65535 之间的整数");
    expect(mocks.forwardCreateRemote).not.toHaveBeenCalled();
  });

  it("reports an invalid remote listen address on blur and clears it immediately after correction", async () => {
    mountForward();
    await flush();
    switchToRemote();
    const bindHost = inputBy('input[aria-label="远端监听地址"]');
    setInputValue(bindHost, "");
    blur(bindHost);
    expect(bindHost.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(bindHost)?.textContent).toBe("远端监听地址不能为空");
    setInputValue(bindHost, "0.0.0.0");
    blur(bindHost);
    expect(bindHost.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(bindHost)).toBeNull();
  });

  it("calls createRemote with the bind address and server-side target for valid input", async () => {
    mountForward();
    await flush();
    switchToRemote();
    setInputValue(inputBy('input[aria-label="远端监听地址"]'), "0.0.0.0");
    setInputValue(inputBy('input[placeholder="18080"]'), "18080");
    setInputValue(inputBy('input[aria-label="目标主机"]'), "127.0.0.1");
    setInputValue(inputBy('input[aria-label="目标端口"]'), "3306");
    clickButton(mounted!.container, "创建");
    await flush();
    expect(mocks.forwardCreateRemote).toHaveBeenCalledTimes(1);
    expect(mocks.forwardCreateRemote).toHaveBeenCalledWith("s", "0.0.0.0", 18080, "127.0.0.1", 3306);
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith(
      "success",
      "远程转发已就绪 → 远端 0.0.0.0:18080 → NexTerm 所在机器 127.0.0.1:3306",
    );
  });

  it("requires needs_confirm for non-loopback binding and retries with acknowledgeRisk after confirmation", async () => {
    mocks.forwardCreateRemote
      .mockRejectedValueOnce(
        Object.assign(new Error("Confirmation required"), { code: "needs_confirm", detail: { listenHost: "0.0.0.0" } }),
      )
      .mockResolvedValueOnce({ id: "f1", kind: "remote" });
    mountForward();
    await flush();
    switchToRemote();
    setInputValue(inputBy('input[aria-label="远端监听地址"]'), "0.0.0.0");
    setInputValue(inputBy('input[placeholder="18080"]'), "18080");
    setInputValue(inputBy('input[aria-label="目标主机"]'), "127.0.0.1");
    setInputValue(inputBy('input[aria-label="目标端口"]'), "3306");
    clickButton(mounted!.container, "创建");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("远端监听 0.0.0.0:18080"),
      expect.objectContaining({ title: "确认远程转发风险" }),
    );
    expect(mocks.forwardCreateRemote).toHaveBeenCalledTimes(2);
    expect(mocks.forwardCreateRemote).toHaveBeenLastCalledWith("s", "0.0.0.0", 18080, "127.0.0.1", 3306, true);
    expect(mocks.toast).toHaveBeenCalledWith(
      "success",
      "远程转发已就绪 → 远端 0.0.0.0:18080 → NexTerm 所在机器 127.0.0.1:3306",
    );
  });

  it("does not retry or claim success when the remote exposure confirmation is rejected", async () => {
    mocks.forwardCreateRemote.mockRejectedValueOnce(
      Object.assign(new Error("Confirmation required"), { code: "needs_confirm", detail: { listenHost: "0.0.0.0" } }),
    );
    mocks.ask.mockResolvedValue(false);
    mountForward();
    await flush();
    switchToRemote();
    setInputValue(inputBy('input[aria-label="远端监听地址"]'), "0.0.0.0");
    setInputValue(inputBy('input[placeholder="18080"]'), "18080");
    setInputValue(inputBy('input[aria-label="目标主机"]'), "127.0.0.1");
    setInputValue(inputBy('input[aria-label="目标端口"]'), "3306");
    clickButton(mounted!.container, "创建");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("远端监听 0.0.0.0:18080"),
      expect.objectContaining({ title: "确认远程转发风险" }),
    );
    expect(mocks.forwardCreateRemote).toHaveBeenCalledTimes(1);
    expect(mocks.forwardCreateRemote).toHaveBeenCalledWith("s", "0.0.0.0", 18080, "127.0.0.1", 3306);
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.any(String));
    await flush();
    expect(mocks.forwardCreateRemote).toHaveBeenCalledTimes(1);
  });

  it("renders remote forwarding entries with type badges and remote listen addresses", async () => {
    mocks.forwardList.mockResolvedValue([
      {
        id: "f1",
        sessionId: "s",
        listenHost: "127.0.0.1",
        listenPort: 18080,
        targetHost: "127.0.0.1",
        targetPort: 3306,
        kind: "remote",
        createdAt: 1,
      },
    ]);
    mountForward();
    await flush();
    const row = mounted!.container.querySelector("tbody tr");
    expect(row?.textContent).toContain("远程转发");
    expect(row?.textContent).toContain("127.0.0.1:18080");
    expect(row?.textContent).toContain("NexTerm 所在机器");
    expect(mounted!.container.textContent).toContain("0 条静态转发 · 1 条远程转发 · 0 条 SOCKS5 代理");
  });

  it("reports stop failures without claiming success", async () => {
    mocks.forwardList.mockResolvedValue([
      {
        id: "f1",
        sessionId: "s",
        listenHost: "127.0.0.1",
        listenPort: 18080,
        targetHost: "127.0.0.1",
        targetPort: 3306,
        kind: "remote",
        createdAt: 1,
      },
    ]);
    mocks.forwardRemove.mockRejectedValue({
      code: "io",
      message: "Failed to close the forwarding listener (the remote port may still be listening): ssh: cancel-tcpip-forward failed",
    });
    mountForward();
    await flush();
    clickButton(mounted!.container, "停止");
    await flush();
    expect(mocks.ask).toHaveBeenCalled();
    expect(mocks.forwardRemove).toHaveBeenCalledWith("f1");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("停止失败"));
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.anything());
  });
});

function fakeSettingsRules(): ForwardRuleSettings[] {
  return [
    {
      id: "rule-a", assetId: "asset-1", name: "database", listenPort: 13306,
      targetHost: "db.internal", targetPort: 5432, kind: "local", listenHost: "127.0.0.1",
      autoStart: false, status: "stopped",
    },
    {
      id: "rule-b", assetId: "asset-1", name: "cache", listenPort: 16379,
      targetHost: "cache.internal", targetPort: 6379, kind: "local", listenHost: "127.0.0.1",
      autoStart: true, status: "stopped",
    },
  ];
}

function fakeSettingsApi(overrides: Partial<ForwardSettingsApi> = {}) {
  return {
    listRules: vi.fn().mockResolvedValue(fakeSettingsRules()),
    setRuleAutoStart: vi.fn().mockResolvedValue(undefined),
    getRecoverySettings: vi.fn().mockResolvedValue({
      enabled: false,
      maxConcurrentConnections: 4,
      skipProduction: true,
    } satisfies ForwardRecoverySettings),
    setRecoverySettings: vi.fn().mockResolvedValue(undefined),
    recoverNow: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

function settingsTree(api: ForwardSettingsApi) {
  return createElement(ForwardSettingsApiProvider, { api }, createElement(ForwardSettingsPanel));
}

function mountSettings(api: ForwardSettingsApi): MountedView {
  return mount(settingsTree(api));
}

function rerenderSettings(mounted: MountedView, api: ForwardSettingsApi): void {
  act(() => {
    mounted.root.render(settingsTree(api));
  });
}

function settingsSwitch(container: HTMLElement, label: string): HTMLButtonElement {
  const button = container.querySelector<HTMLButtonElement>(`button[role="switch"][aria-label="${label}"]`);
  if (!button) throw new Error(`settings switch not found: ${label}`);
  return button;
}

describe("Forward settings", () => {
  let settingsMounted: MountedView | undefined;
  beforeEach(() => {
    settingsMounted = undefined;
  });
  afterEach(() => {
    settingsMounted?.unmount();
    settingsMounted = undefined;
  });

  it("renders saved rules with their autoStart state and toggles it through the injected API", async () => {
    const api = fakeSettingsApi();
    settingsMounted = mountSettings(api);
    await waitFor(() => expect(api.listRules).toHaveBeenCalledOnce());

    const off = settingsSwitch(settingsMounted.container, "自动启动 database");
    const on = settingsSwitch(settingsMounted.container, "自动启动 cache");
    expect(off.getAttribute("aria-checked")).toBe("false");
    expect(on.getAttribute("aria-checked")).toBe("true");
    expect(settingsMounted.container.textContent).toContain("127.0.0.1:13306 → db.internal:5432");
    expect(settingsMounted.container.textContent).toContain("127.0.0.1:16379 → cache.internal:6379");

    clickButton(settingsMounted.container, "已关闭");
    await waitFor(() => expect(api.setRuleAutoStart).toHaveBeenCalledWith("rule-a", true));
    expect(settingsSwitch(settingsMounted.container, "自动启动 database").getAttribute("aria-checked")).toBe("true");
  });

  it("presents startup recovery as default-off with the real concurrency cap and production skip", async () => {
    const api = fakeSettingsApi();
    settingsMounted = mountSettings(api);
    await waitFor(() => expect(api.getRecoverySettings).toHaveBeenCalledOnce());

    const toggle = settingsSwitch(settingsMounted.container, "启动时自动恢复转发");
    expect(toggle.getAttribute("aria-checked")).toBe("false");
    expect(settingsMounted.container.textContent).toContain("默认关闭");
    expect(settingsMounted.container.textContent).toContain("最多同时建立 4 个设备连接");
    expect(settingsMounted.container.textContent).toContain("自动跳过生产项目资产");
    expect(settingsMounted.container.textContent).toContain("仅本地固定端口转发，只监听 127.0.0.1");
    expect(settingsMounted.container.textContent).toContain("SOCKS5、远程转发和公共监听不支持自动启动");
  });

  it("enabling startup recovery persists enabled with the real semantics through the injected API", async () => {
    const api = fakeSettingsApi();
    settingsMounted = mountSettings(api);
    await waitFor(() => expect(api.getRecoverySettings).toHaveBeenCalledOnce());

    click(settingsSwitch(settingsMounted.container, "启动时自动恢复转发"));
    await waitFor(() =>
      expect(api.setRecoverySettings).toHaveBeenCalledWith({
        enabled: true,
        maxConcurrentConnections: 4,
        skipProduction: true,
      }),
    );
    expect(settingsSwitch(settingsMounted.container, "启动时自动恢复转发").getAttribute("aria-checked")).toBe("true");
  });

  it("offers recover-now only when startup recovery is enabled and calls the injected API", async () => {
    const api = fakeSettingsApi();
    settingsMounted = mountSettings(api);
    await waitFor(() => expect(api.getRecoverySettings).toHaveBeenCalledOnce());
    expect([...settingsMounted.container.querySelectorAll("button")].some((b) => b.textContent?.trim() === "立即恢复")).toBe(false);

    click(settingsSwitch(settingsMounted.container, "启动时自动恢复转发"));
    await waitFor(() => expect(api.setRecoverySettings).toHaveBeenCalledOnce());
    clickButton(settingsMounted.container, "立即恢复");
    await waitFor(() => expect(api.recoverNow).toHaveBeenCalledOnce());
  });
});

describe("Forward settings generations and eligibility", () => {
  let settingsMounted: MountedView | undefined;
  beforeEach(() => {
    settingsMounted = undefined;
  });
  afterEach(() => {
    settingsMounted?.unmount();
    settingsMounted = undefined;
  });

  it("keeps the recovery switch disabled until the initial settings load and never clobbers a successful write", async () => {
    const settings = deferred<ForwardRecoverySettings>();
    const api = fakeSettingsApi({
      getRecoverySettings: vi.fn().mockReturnValue(settings.promise),
    });
    settingsMounted = mountSettings(api);

    const pending = settingsSwitch(settingsMounted.container, "启动时自动恢复转发");
    expect(pending.disabled).toBe(true);
    expect(api.setRecoverySettings).not.toHaveBeenCalled();

    settings.resolve({ enabled: false, maxConcurrentConnections: 4, skipProduction: true });
    await act(async () => {
      await settings.promise;
    });
    await waitFor(() =>
      expect(settingsSwitch(settingsMounted!.container, "启动时自动恢复转发").disabled).toBe(false),
    );

    click(settingsSwitch(settingsMounted.container, "启动时自动恢复转发"));
    await waitFor(() =>
      expect(api.setRecoverySettings).toHaveBeenCalledWith({
        enabled: true,
        maxConcurrentConnections: 4,
        skipProduction: true,
      }),
    );
    await flush();
    expect(settingsSwitch(settingsMounted.container, "启动时自动恢复转发").getAttribute("aria-checked")).toBe("true");
  });

  it("hides the previous account's rules immediately on API switch and never submits them to the new API", async () => {
    const apiA = fakeSettingsApi();
    settingsMounted = mountSettings(apiA);
    await waitFor(() => expect(apiA.listRules).toHaveBeenCalledOnce());
    expect(settingsSwitch(settingsMounted.container, "自动启动 database")).toBeDefined();

    const apiB = fakeSettingsApi({
      listRules: vi.fn().mockReturnValue(new Promise<ForwardRuleSettings[]>(() => {})),
      getRecoverySettings: vi.fn().mockReturnValue(new Promise<ForwardRecoverySettings>(() => {})),
    });
    rerenderSettings(settingsMounted, apiB);
    await flush();

    expect(settingsMounted.container.querySelector('button[aria-label="自动启动 database"]')).toBeNull();
    expect(settingsSwitch(settingsMounted.container, "启动时自动恢复转发").disabled).toBe(true);
    expect(apiB.setRuleAutoStart).not.toHaveBeenCalled();
    expect(apiB.setRuleAutoStart).not.toHaveBeenCalledWith("rule-a", expect.anything());
  });

  it("does not let the previous account's late write completion pollute the new generation's display", async () => {
    const write = deferred<void>();
    const apiA = fakeSettingsApi({
      setRecoverySettings: vi.fn().mockReturnValue(write.promise),
    });
    settingsMounted = mountSettings(apiA);
    await waitFor(() => expect(apiA.getRecoverySettings).toHaveBeenCalledOnce());

    click(settingsSwitch(settingsMounted.container, "启动时自动恢复转发"));
    await waitFor(() => expect(apiA.setRecoverySettings).toHaveBeenCalledOnce());

    const apiB = fakeSettingsApi();
    rerenderSettings(settingsMounted, apiB);
    await waitFor(() => expect(apiB.getRecoverySettings).toHaveBeenCalledOnce());
    await flush();
    expect(settingsSwitch(settingsMounted.container, "启动时自动恢复转发").getAttribute("aria-checked")).toBe("false");

    write.resolve(undefined);
    await flush();
    expect(settingsSwitch(settingsMounted.container, "启动时自动恢复转发").getAttribute("aria-checked")).toBe("false");
    expect(apiB.setRecoverySettings).not.toHaveBeenCalled();
  });

  it("does not update state after unmount when a write settles late", async () => {
    const write = deferred<void>();
    const api = fakeSettingsApi({
      setRuleAutoStart: vi.fn().mockReturnValue(write.promise),
    });
    settingsMounted = mountSettings(api);
    await waitFor(() => expect(api.listRules).toHaveBeenCalledOnce());

    click(settingsSwitch(settingsMounted.container, "自动启动 database"));
    await waitFor(() => expect(api.setRuleAutoStart).toHaveBeenCalledOnce());
    settingsMounted.unmount();
    settingsMounted = undefined;
    write.resolve(undefined);
    await flush();
  });

  it("offers autoStart only for local strictly-loopback fixed-port rules", async () => {
    const rules: ForwardRuleSettings[] = [
      {
        id: "local-ok", assetId: "asset-1", name: "db", listenPort: 13306,
        targetHost: "db.internal", targetPort: 5432, kind: "local", listenHost: "127.0.0.1",
        autoStart: false, status: "stopped",
      },
      {
        id: "remote-rule", assetId: "asset-1", name: "remote-db", listenPort: 18080,
        targetHost: "127.0.0.1", targetPort: 3306, kind: "remote", listenHost: "0.0.0.0",
        autoStart: false, status: "stopped",
      },
      {
        id: "socks-rule", assetId: "asset-1", name: "proxy", listenPort: 1080,
        targetHost: "", targetPort: 0, kind: "socks", listenHost: "::",
        autoStart: false, status: "stopped",
      },
      {
        id: "local-public", assetId: "asset-1", name: "public-db", listenPort: 13307,
        targetHost: "db.internal", targetPort: 5432, kind: "local", listenHost: "0.0.0.0",
        autoStart: false, status: "stopped",
      },
      {
        id: "local-localhost", assetId: "asset-1", name: "named-db", listenPort: 13308,
        targetHost: "db.internal", targetPort: 5432, kind: "local", listenHost: "localhost",
        autoStart: false, status: "stopped",
      },
    ];
    const api = fakeSettingsApi({ listRules: vi.fn().mockResolvedValue(rules) });
    settingsMounted = mountSettings(api);
    await waitFor(() => expect(api.listRules).toHaveBeenCalledOnce());

    expect(settingsSwitch(settingsMounted.container, "自动启动 db").getAttribute("aria-checked")).toBe("false");
    for (const label of ["自动启动 remote-db", "自动启动 proxy", "自动启动 public-db", "自动启动 named-db"]) {
      expect(settingsMounted.container.querySelector(`button[aria-label="${label}"]`)).toBeNull();
    }
    expect(settingsMounted.container.textContent).toContain("0.0.0.0:18080");
    expect(settingsMounted.container.textContent).not.toContain("127.0.0.1:18080");
    expect(api.setRuleAutoStart).not.toHaveBeenCalled();
  });

  it("disables recover-now after the first click until the in-flight recovery settles", async () => {
    const recover = deferred<void>();
    const api = fakeSettingsApi({
      getRecoverySettings: vi.fn().mockResolvedValue({
        enabled: true,
        maxConcurrentConnections: 4,
        skipProduction: true,
      } satisfies ForwardRecoverySettings),
      recoverNow: vi.fn().mockReturnValue(recover.promise),
    });
    settingsMounted = mountSettings(api);
    await waitFor(() => expect(api.getRecoverySettings).toHaveBeenCalledOnce());

    clickButton(settingsMounted.container, "立即恢复");
    await waitFor(() => expect(api.recoverNow).toHaveBeenCalledOnce());
    const again = [...settingsMounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "立即恢复",
    );
    expect(again?.disabled).toBe(true);
    click(again as HTMLButtonElement);
    await flush();
    expect(api.recoverNow).toHaveBeenCalledOnce();

    recover.resolve(undefined);
    await act(async () => {
      await recover.promise;
    });
    await waitFor(() =>
      expect(
        [...settingsMounted!.container.querySelectorAll("button")].find(
          (b) => b.textContent?.trim() === "立即恢复",
        )?.disabled,
      ).toBe(false),
    );
  });
});
