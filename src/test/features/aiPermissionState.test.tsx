/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { deferred, flush, mount, type MountedView } from "./reactTestUtils";
import type { AiPermissionMode, AiPermissionState } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({ permissionState: vi.fn(), setPermission: vi.fn(), toast: vi.fn() }));

vi.mock("../../ipc/commands", () => ({
  aiApi: { permissionState: mocks.permissionState, setPermission: mocks.setPermission },
}));

import { useAiPermission } from "../../features/ai/panel/useAiPermission";

type Hook = ReturnType<typeof useAiPermission>;
type Scope = Record<string, unknown> | undefined;

function stateOf(globalMode: AiPermissionMode, hostMode?: AiPermissionMode): AiPermissionState {
  return {
    config: { mode: hostMode ?? globalMode, dangerRules: ["rule"] },
    globalMode,
    hostMode,
    hostResolved: true,
    source: hostMode ? "host" : "global",
  };
}

// A minimal backend: one global mode plus explicit overrides keyed by session.
function fakeBackend(globalMode: AiPermissionMode, overrides: Record<string, AiPermissionMode> = {}) {
  const backend = { globalMode, overrides };
  const read = (scope: Scope) => stateOf(backend.globalMode, overrides[String(scope?.sessionId ?? "")]);
  mocks.permissionState.mockImplementation((scope: Scope) => Promise.resolve(read(scope)));
  mocks.setPermission.mockImplementation(
    (config: { mode: AiPermissionMode }, scope: Scope, options?: { hostOnly?: boolean; followGlobal?: boolean }) => {
      const host = String(scope?.sessionId ?? "");
      if (options?.hostOnly) overrides[host] = config.mode;
      else if (options?.followGlobal) delete overrides[host];
      else backend.globalMode = config.mode;
      return Promise.resolve(read(scope));
    },
  );
  return backend;
}

const views: MountedView[] = [];

function renderHook(scope: Scope) {
  const current: { hook: Hook | null; setScope: (scope: Scope) => void } = { hook: null, setScope: () => undefined };
  function Probe({ scope }: { scope: Scope }) {
    current.hook = useAiPermission(scope, mocks.toast);
    return null;
  }
  const view = mount(createElement(Probe, { scope }));
  views.push(view);
  current.setScope = (next) => act(() => view.root.render(createElement(Probe, { scope: next })));
  return current;
}

const hostA = { sessionId: "a", tabId: "ta" };
const hostB = { sessionId: "b", tabId: "tb" };

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  for (const view of views.splice(0)) view.unmount();
});

describe("useAiPermission", () => {
  it("switches the global mode from a terminal and keeps it after switching tabs", async () => {
    const backend = fakeBackend("silent");
    const panel = renderHook(hostA);
    await flush();
    expect(panel.hook?.perm?.mode).toBe("silent");

    act(() => panel.hook!.switchMode("full_access"));
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledWith({ mode: "full_access", dangerRules: ["rule"] }, hostA, undefined);
    expect(backend.globalMode).toBe("full_access");
    expect(panel.hook?.perm?.mode).toBe("full_access");

    panel.setScope(hostB);
    await flush();
    expect(panel.hook?.perm?.mode).toBe("full_access");
    panel.setScope(hostA);
    await flush();
    expect(panel.hook?.perm?.mode).toBe("full_access");
    expect(panel.hook?.permState?.source).toBe("global");
  });

  it("ignores a late response for a terminal the panel already left", async () => {
    fakeBackend("full_access", { b: "read_only" });
    const late = deferred<AiPermissionState>();
    const panel = renderHook(hostA);
    await flush();
    mocks.permissionState.mockImplementationOnce(() => late.promise);
    panel.setScope(hostB);
    panel.setScope(hostA);
    await flush();
    expect(panel.hook?.perm?.mode).toBe("full_access");
    await act(async () => {
      late.resolve(stateOf("full_access", "read_only"));
      await late.promise;
    });
    expect(panel.hook?.perm?.mode).toBe("full_access");
  });

  it("edits an explicit host override in place and reports where the mode comes from", async () => {
    const backend = fakeBackend("read_write", { a: "read_only" });
    const panel = renderHook(hostA);
    await flush();
    expect(panel.hook?.permState).toMatchObject({ source: "host", hostMode: "read_only", globalMode: "read_write" });

    act(() => panel.hook!.switchMode("silent"));
    await flush();
    expect(mocks.setPermission).toHaveBeenLastCalledWith(expect.objectContaining({ mode: "silent" }), hostA, { hostOnly: true });
    expect(backend.globalMode).toBe("read_write");
    expect(panel.hook?.perm?.mode).toBe("silent");

    panel.setScope(hostB);
    await flush();
    expect(panel.hook?.permState).toMatchObject({ source: "global", config: { mode: "read_write" } });

    panel.setScope(hostA);
    await flush();
    act(() => panel.hook!.setHostOverride(false));
    await flush();
    expect(mocks.setPermission).toHaveBeenLastCalledWith(expect.anything(), hostA, { followGlobal: true });
    expect(panel.hook?.permState).toMatchObject({ source: "global", config: { mode: "read_write" } });
  });

  it("creates a host override only on request, starting from the mode in force", async () => {
    fakeBackend("silent");
    const panel = renderHook(hostA);
    await flush();
    act(() => panel.hook!.setHostOverride(true));
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledWith(expect.objectContaining({ mode: "silent" }), hostA, { hostOnly: true });
    expect(panel.hook?.permState).toMatchObject({ source: "host", hostMode: "silent" });
  });

  it("refreshes every mounted panel after a save", async () => {
    fakeBackend("read_write");
    const first = renderHook(hostA);
    const second = renderHook(hostB);
    await flush();
    act(() => first.hook!.switchMode("read_only"));
    await flush();
    expect(second.hook?.perm?.mode).toBe("read_only");
  });

  it("restores the shown mode and reports a failed save", async () => {
    fakeBackend("read_write");
    const panel = renderHook(hostA);
    await flush();
    mocks.setPermission.mockRejectedValueOnce(new Error("disk full"));
    act(() => panel.hook!.switchMode("silent"));
    await flush();
    expect(panel.hook?.perm?.mode).toBe("read_write");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("disk full"));
  });
});
