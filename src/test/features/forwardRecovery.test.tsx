/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { flush, mount, waitFor, deferred, type MountedView } from "./reactTestUtils";
import { useAuth } from "../../features/auth/store";
import { useUi, type Workspace } from "../../app/store";
import {
  ForwardStartupRecovery,
  type ForwardStartupRecoveryApi,
} from "../../features/forward/ForwardStartupRecovery";
import type { ForwardRecoverySettings } from "../../features/forward/forwardSettingsApi";

const DEFAULT_SETTINGS: ForwardRecoverySettings = {
  enabled: false,
  maxConcurrentConnections: 4,
  skipProduction: true,
};

function fakeApi(overrides: Partial<ForwardStartupRecoveryApi> = {}): ForwardStartupRecoveryApi {
  return {
    getRecoverySettings: vi.fn().mockResolvedValue(DEFAULT_SETTINGS),
    recoverNow: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

let mounted: MountedView | undefined;
let toast: Mock<(kind: "info" | "error" | "success", text: string) => void>;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  toast = vi.fn();
  useAuth.setState({ gate: "ready" });
  useUi.setState({ accountEpoch: 0, pushToast: toast, toasts: [], workspaces: [] });
});

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  await flush();
});

function mountRecovery(api: ForwardStartupRecoveryApi): void {
  mounted = mount(createElement(ForwardStartupRecovery, { api }));
}

describe("ForwardStartupRecovery", () => {
  it("does not recover when the persisted per-device opt-in is default-off", async () => {
    const api = fakeApi();
    mountRecovery(api);

    await waitFor(() => expect(api.getRecoverySettings).toHaveBeenCalledOnce());
    await flush();

    expect(api.recoverNow).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("fails closed without a startup toast when the persisted preference cannot be read", async () => {
    const api = fakeApi({ getRecoverySettings: vi.fn().mockRejectedValue(new Error("settings unavailable")) });
    mountRecovery(api);

    await waitFor(() => expect(api.getRecoverySettings).toHaveBeenCalledOnce());
    await flush();

    expect(api.recoverNow).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("runs the existing recovery IPC once for an enabled startup without reconnecting restored tabs", async () => {
    const api = fakeApi({
      getRecoverySettings: vi.fn().mockResolvedValue({ ...DEFAULT_SETTINGS, enabled: true }),
    });
    const restored: Workspace = {
      id: "ws-restored",
      kind: "session",
      title: "restored host",
      sessionId: "session-restored",
      closable: true,
      panes: [{
        id: "pane-restored",
        activeTabId: "tab-restored",
        tabs: [{
          id: "tab-restored",
          kind: "terminal",
          title: "restored terminal",
          sessionId: "session-restored",
          restorePending: true,
          closable: true,
        }],
      }],
      activePaneId: "pane-restored",
    };
    useUi.setState({ workspaces: [restored] });
    const workspaces = useUi.getState().workspaces;
    mountRecovery(api);

    await waitFor(() => expect(api.recoverNow).toHaveBeenCalledOnce());
    await flush();

    expect(api.getRecoverySettings).toHaveBeenCalledOnce();
    expect(api.recoverNow).toHaveBeenCalledOnce();
    expect(useUi.getState().workspaces).toBe(workspaces);
    expect(useUi.getState().workspaces[0].panes[0].tabs[0].restorePending).toBe(true);
    expect(toast).not.toHaveBeenCalled();
  });

  it("drops a previous account generation's late settings before starting the new generation", async () => {
    const first = deferred<ForwardRecoverySettings>();
    const second = deferred<ForwardRecoverySettings>();
    const getRecoverySettings = vi.fn()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
    const api = fakeApi({ getRecoverySettings });
    mountRecovery(api);
    await waitFor(() => expect(getRecoverySettings).toHaveBeenCalledOnce());

    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    await waitFor(() => expect(getRecoverySettings).toHaveBeenCalledTimes(2));

    first.resolve({ ...DEFAULT_SETTINGS, enabled: true });
    await flush();
    expect(api.recoverNow).not.toHaveBeenCalled();

    second.resolve({ ...DEFAULT_SETTINGS, enabled: true });
    await waitFor(() => expect(api.recoverNow).toHaveBeenCalledOnce());
    expect(toast).not.toHaveBeenCalled();
  });

  it("cancels a pending startup read on unmount instead of recovering or reporting later", async () => {
    const settings = deferred<ForwardRecoverySettings>();
    const api = fakeApi({ getRecoverySettings: vi.fn().mockReturnValue(settings.promise) });
    mountRecovery(api);
    await waitFor(() => expect(api.getRecoverySettings).toHaveBeenCalledOnce());

    mounted?.unmount();
    mounted = undefined;
    settings.resolve({ ...DEFAULT_SETTINGS, enabled: true });
    await flush();

    expect(api.recoverNow).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("suppresses a late failure after cancellation but keeps current failures visible", async () => {
    const recovery = deferred<void>();
    const api = fakeApi({
      getRecoverySettings: vi.fn().mockResolvedValue({ ...DEFAULT_SETTINGS, enabled: true }),
      recoverNow: vi.fn().mockReturnValue(recovery.promise),
    });
    mountRecovery(api);
    await waitFor(() => expect(api.recoverNow).toHaveBeenCalledOnce());

    mounted?.unmount();
    mounted = undefined;
    recovery.reject(new Error("old account dial failed"));
    await flush();
    expect(toast).not.toHaveBeenCalled();

    const current = fakeApi({
      getRecoverySettings: vi.fn().mockResolvedValue({ ...DEFAULT_SETTINGS, enabled: true }),
      recoverNow: vi.fn().mockRejectedValue(new Error("dial failed")),
    });
    mountRecovery(current);
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith("error", expect.stringContaining("启动恢复失败：dial failed")),
    );
  });

  it("waits for Web authentication readiness before reading recovery settings", async () => {
    useAuth.setState({ gate: "loading" });
    const api = fakeApi();
    mountRecovery(api);
    await flush();
    expect(api.getRecoverySettings).not.toHaveBeenCalled();

    act(() => {
      useAuth.setState({ gate: "ready" });
    });
    await waitFor(() => expect(api.getRecoverySettings).toHaveBeenCalledOnce());
  });
});
