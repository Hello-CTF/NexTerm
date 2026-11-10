/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, deferred, flush, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  vaultStatus: vi.fn(),
  listCredentials: vi.fn(),
  revealCredential: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    vaultApi: {
      status: mocks.vaultStatus,
      listCredentials: mocks.listCredentials,
      revealCredential: mocks.revealCredential,
      lock: vi.fn(),
      unlock: vi.fn(),
      setCredential: vi.fn(),
      updateCredential: vi.fn(),
      deleteCredential: vi.fn(),
    },
    assetApi: { list: vi.fn().mockResolvedValue([]) },
    sessionApi: {},
    terminalApi: {},
  };
});

import { CredentialsPanel } from "../../features/credentials/CredentialsPanel";
import { useUi } from "../../app/store";

const CRED = { id: "c1", name: "db-prod", kind: "password", usedBy: [], createdAt: 1, updatedAt: 1 };
const SECRET = { value: "s3cret", passphrase: "" };

let mounted: MountedView | undefined;
let liveIntervals: Set<number>;
beforeEach(() => {
  vi.useFakeTimers();
  liveIntervals = new Set();
  const setIntervalImpl = window.setInterval.bind(window);
  const clearIntervalImpl = window.clearInterval.bind(window);
  vi.spyOn(window, "setInterval").mockImplementation(((handler: TimerHandler, ms?: number) => {
    const id = setIntervalImpl(handler, ms);
    liveIntervals.add(id);
    return id;
  }) as typeof window.setInterval);
  vi.spyOn(window, "clearInterval").mockImplementation((id?: number) => {
    if (id !== undefined) liveIntervals.delete(id);
    clearIntervalImpl(id);
  });
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true, mode: "off" });
  mocks.listCredentials.mockResolvedValue([CRED]);
  useUi.setState({ pushToast: mocks.toast, sessions: [], appDialog: null });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
  vi.useRealTimers();
});

function text(): string {
  return mounted?.container.textContent ?? "";
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

async function renderPanel(): Promise<void> {
  mounted = mountWithClient(createElement(CredentialsPanel, { credId: "c1" }));
  await flushUntil(() => text().includes("显示"));
}

describe("CredentialDetail reveal countdown", () => {
  it("a second click while the reveal is pending starts no second countdown", async () => {
    const pending = deferred<typeof SECRET>();
    mocks.revealCredential.mockReturnValueOnce(pending.promise);
    await renderPanel();

    clickButton(mounted!.container, "显示");
    clickButton(mounted!.container, "显示");
    await act(async () => {
      pending.resolve(SECRET);
      await pending.promise;
    });
    await flushUntil(() => text().includes("s3cret"));
    expect(mocks.revealCredential).toHaveBeenCalledTimes(1);
    expect(text()).toContain("15 秒后自动隐藏");

    await advance(5000);
    expect(text()).toContain("10 秒后自动隐藏");

    await advance(10000);
    expect(text()).not.toContain("s3cret");
    expect(liveIntervals.size).toBe(0);
  });

  it("a later reveal gets its full countdown", async () => {
    mocks.revealCredential.mockResolvedValue(SECRET);
    await renderPanel();

    clickButton(mounted!.container, "显示");
    await flushUntil(() => text().includes("s3cret"));
    clickButton(mounted!.container, "隐藏");
    expect(text()).not.toContain("s3cret");

    clickButton(mounted!.container, "显示");
    await flushUntil(() => text().includes("s3cret"));
    await advance(3000);
    expect(text()).toContain("s3cret");
    expect(text()).toContain("12 秒后自动隐藏");
  });

  it("unmounting clears the countdown, also when the reveal resolves afterwards", async () => {
    mocks.revealCredential.mockResolvedValue(SECRET);
    await renderPanel();
    clickButton(mounted!.container, "显示");
    await flushUntil(() => text().includes("s3cret"));
    mounted!.unmount();
    mounted = undefined;
    expect(liveIntervals.size).toBe(0);

    const pending = deferred<typeof SECRET>();
    mocks.revealCredential.mockReturnValueOnce(pending.promise);
    await renderPanel();
    clickButton(mounted!.container, "显示");
    mounted!.unmount();
    mounted = undefined;
    await act(async () => {
      pending.resolve(SECRET);
      await pending.promise;
    });
    await flush();
    expect(liveIntervals.size).toBe(0);
  });
});
