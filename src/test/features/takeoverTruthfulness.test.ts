import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  cancel: vi.fn(),
  exit: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  aiApi: { cancel: mocks.cancel, takeoverExit: mocks.exit },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));

import { stealBack } from "../../app/TakeoverBanner";
import { useUi, type TakeoverState } from "../../app/store";

function takeover(startedAt = 1): TakeoverState {
  return { tabId: "tab", jobId: "job", token: "token", task: "task", allowWrite: true, startedAt };
}

describe("takeover steal-back truthfulness", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cancel.mockResolvedValue(undefined);
    mocks.exit.mockResolvedValue(undefined);
    useUi.setState({ takeover: null, pushToast: mocks.toast });
  });

  it("retains the banner when takeover exit fails", async () => {
    const current = takeover();
    useUi.getState().setTakeover(current);
    mocks.exit.mockRejectedValue(new Error("offline"));
    await stealBack();
    expect(useUi.getState().takeover).toBe(current);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringMatching(/可能仍在操作/));
  });

  it("clears the banner only after successful exit", async () => {
    useUi.getState().setTakeover(takeover());
    await stealBack();
    expect(mocks.exit).toHaveBeenCalledWith("tab", "token", "用户收回控制权");
    expect(useUi.getState().takeover).toBeNull();
  });

  it("does not clear a newer takeover when an older exit completes", async () => {
    const original = takeover(1);
    const newer = takeover(2);
    let release!: () => void;
    mocks.exit.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    useUi.getState().setTakeover(original);
    const pending = stealBack();
    await vi.waitFor(() => expect(mocks.exit).toHaveBeenCalledOnce());
    useUi.getState().setTakeover(newer);
    release();
    await pending;
    expect(useUi.getState().takeover).toBe(newer);
  });

  it("deduplicates concurrent Esc/click requests and still exits after cancel failure", async () => {
    let release!: () => void;
    mocks.cancel.mockRejectedValue(new Error("cancel failed"));
    mocks.exit.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    useUi.getState().setTakeover(takeover());
    const first = stealBack();
    const second = stealBack();
    await vi.waitFor(() => expect(mocks.exit).toHaveBeenCalledOnce());
    release();
    await Promise.all([first, second]);
    expect(mocks.exit).toHaveBeenCalledOnce();
    expect(useUi.getState().takeover).toBeNull();
  });
});
