/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { UpdateStatusDto } from "../ipc/types";
import type { ChoiceOptions } from "../ui/dialogs";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    check: vi.fn(),
    install: vi.fn(),
    restart: vi.fn(),
    askChoice: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    appUpdateApi: { check: mocks.check, install: mocks.install, restart: mocks.restart },
  };
});

vi.mock("../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/events")>();
  return {
    ...actual,
    listenEvent: () => Promise.resolve(() => {}),
  };
});

vi.mock("../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/dialogs")>();
  return { ...actual, askChoice: mocks.askChoice };
});

import { isReleaseVersion, promptUpdateIfNeeded } from "../app/updatePrompt";
import { useUpdateStore } from "../app/useUpdate";

function makeStatus(patch: Partial<UpdateStatusDto> = {}): UpdateStatusDto {
  return {
    available: true,
    currentVersion: "0.2.2",
    version: "0.3.0",
    notes: "",
    prerelease: false,
    assetName: "nexterm-desktop_linux_amd64_0.3.0.deb",
    assetSize: 8 << 20,
    canInstall: true,
    unavailableReason: "",
    ...patch,
  };
}

function lastChoiceCall(): [string, ChoiceOptions] {
  const call = mocks.askChoice.mock.calls.at(-1);
  if (!call) throw new Error("askChoice was not called");
  return call as [string, ChoiceOptions];
}

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  useUpdateStore.setState({
    status: null,
    checking: false,
    checkError: null,
    install: { active: false, phase: null, transferred: 0, total: 0, error: null, taskId: null },
    installedVersion: null,
    restarting: false,
    restartError: null,
    skippedVersion: null,
    remindedVersion: null,
  });
});

describe("isReleaseVersion placeholder and parseability gating", () => {
  it("accepts regular and prerelease versions", () => {
    expect(isReleaseVersion("0.2.2")).toBe(true);
    expect(isReleaseVersion("v1.0.0")).toBe(true);
    expect(isReleaseVersion("0.2.2-rc.7")).toBe(true);
  });

  it("rejects dev/0.0.0 placeholders and unparseable versions", () => {
    expect(isReleaseVersion("dev")).toBe(false);
    expect(isReleaseVersion("0.0.0")).toBe(false);
    expect(isReleaseVersion("")).toBe(false);
    expect(isReleaseVersion("not-a-version")).toBe(false);
  });
});

describe("update prompt dialog", () => {
  it("offers update (primary), skip current version, and later when a new version is available", async () => {
    mocks.askChoice.mockResolvedValue(null);
    useUpdateStore.setState({ status: makeStatus() });
    await promptUpdateIfNeeded();
    expect(mocks.askChoice).toHaveBeenCalledTimes(1);
    const [message, options] = lastChoiceCall();
    expect(message).toContain("v0.3.0");
    expect(message).toContain("v0.2.2");
    expect(options.title).toBe("软件更新");
    expect(options.choices.map((c) => c.key)).toEqual(["update", "skip", "later"]);
    expect(options.choices[0]?.label).toBe("更新");
    expect(options.choices[0]?.primary).toBe(true);
    expect(options.choices[0]?.hint).toContain("v0.3.0");
    expect(options.choices[1]?.label).toBe("跳过当前版本");
    expect(options.choices[1]?.hint).toContain("不再提醒");
    expect(options.choices[2]?.label).toBe("稍后再说");
  });

  it("choosing update uses the existing installation flow", async () => {
    mocks.askChoice.mockResolvedValue("update");
    mocks.install.mockResolvedValue({
      installed: true,
      restarted: false,
      version: "0.3.0",
      unavailableReason: "",
    });
    useUpdateStore.setState({ status: makeStatus() });
    await promptUpdateIfNeeded();
    expect(mocks.install).toHaveBeenCalledWith("0.3.0");
    expect(window.localStorage.getItem("nexterm.update.v1")).toBeNull();
  });

  it("choosing skip persists the preference and suppresses the same version", async () => {
    mocks.askChoice.mockResolvedValue("skip");
    useUpdateStore.setState({ status: makeStatus() });
    await promptUpdateIfNeeded();
    expect(window.localStorage.getItem("nexterm.update.v1")).toBe("0.3.0");
    expect(useUpdateStore.getState().skippedVersion).toBe("0.3.0");
    expect(mocks.install).not.toHaveBeenCalled();

    mocks.askChoice.mockClear();
    await promptUpdateIfNeeded();
    expect(mocks.askChoice).not.toHaveBeenCalled();
  });

  it("choosing later does not persist a preference and suppresses the same version for this launch", async () => {
    mocks.askChoice.mockResolvedValue("later");
    useUpdateStore.setState({ status: makeStatus() });
    await promptUpdateIfNeeded();
    expect(window.localStorage.getItem("nexterm.update.v1")).toBeNull();
    expect(useUpdateStore.getState().skippedVersion).toBeNull();
    expect(useUpdateStore.getState().remindedVersion).toBe("0.3.0");

    mocks.askChoice.mockClear();
    await promptUpdateIfNeeded();
    expect(mocks.askChoice).not.toHaveBeenCalled();
  });

  it("Esc/cancel (null) behaves like later and only dismisses this prompt", async () => {
    mocks.askChoice.mockResolvedValue(null);
    useUpdateStore.setState({ status: makeStatus() });
    await promptUpdateIfNeeded();
    expect(window.localStorage.getItem("nexterm.update.v1")).toBeNull();
    expect(useUpdateStore.getState().skippedVersion).toBeNull();
    expect(mocks.install).not.toHaveBeenCalled();
    expect(useUpdateStore.getState().remindedVersion).toBe("0.3.0");
  });

  it("prompts again for a newer version after choosing later", async () => {
    mocks.askChoice.mockResolvedValue("later");
    useUpdateStore.setState({ status: makeStatus() });
    await promptUpdateIfNeeded();
    useUpdateStore.setState({ status: makeStatus({ version: "0.3.1" }) });
    await promptUpdateIfNeeded();
    expect(mocks.askChoice).toHaveBeenCalledTimes(2);
  });

  it("does not prompt for placeholder or unparseable current versions", async () => {
    for (const currentVersion of ["dev", "0.0.0", "", "not-a-version"]) {
      useUpdateStore.setState({ status: makeStatus({ currentVersion }), remindedVersion: null });
      await promptUpdateIfNeeded();
    }
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(useUpdateStore.getState().remindedVersion).toBeNull();
  });

  it("does not prompt when there is no update or no completed check", async () => {
    await promptUpdateIfNeeded();
    useUpdateStore.setState({ status: makeStatus({ available: false, version: "" }) });
    await promptUpdateIfNeeded();
    expect(mocks.askChoice).not.toHaveBeenCalled();
  });

  it("does not prompt for skipped versions and restores skip preferences from localStorage on cold start", async () => {
    useUpdateStore.setState({ status: makeStatus(), skippedVersion: "0.3.0" });
    await promptUpdateIfNeeded();
    expect(mocks.askChoice).not.toHaveBeenCalled();

    window.localStorage.setItem("nexterm.update.v1", "0.3.0");
    vi.resetModules();
    const fresh = await import("../app/useUpdate");
    expect(fresh.useUpdateStore.getState().skippedVersion).toBe("0.3.0");
  });
});
