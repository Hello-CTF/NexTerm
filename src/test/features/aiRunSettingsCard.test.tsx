/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  set: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/aiRunSettingsApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/aiRunSettingsApi")>()),
  aiRunSettingsApi: { get: mocks.get, set: mocks.set },
}));

import { AiRunSettingsCard } from "../../features/settings/AiRunSettingsCard";
import { useUi } from "../../app/store";

let view: MountedView | null = null;

function input(): HTMLInputElement {
  const element = view!.container.querySelector<HTMLInputElement>('input[aria-label="单轮最大步数"]');
  if (!element) throw new Error("step limit input not found");
  return element;
}

function saveButton(): HTMLButtonElement {
  const button = [...view!.container.querySelectorAll<HTMLButtonElement>("button")].find((candidate) => candidate.textContent?.trim() === "保存");
  if (!button) throw new Error("save button not found");
  return button;
}

beforeEach(() => {
  mocks.get.mockReset().mockResolvedValue({ maxTurns: 999 });
  mocks.set.mockReset().mockImplementation(async (value: { maxTurns: number }) => value);
  mocks.toast.mockReset();
  useUi.setState({ pushToast: mocks.toast });
});

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("AI step limit setting", () => {
  it("loads the stored limit and saves a value in range", async () => {
    view = mount(createElement(AiRunSettingsCard));
    await flush();
    expect(input().value).toBe("999");
    expect(saveButton().disabled).toBe(true);

    setInputValue(input(), "120");
    expect(saveButton().disabled).toBe(false);
    click(saveButton());
    await flush();

    expect(mocks.set).toHaveBeenCalledWith({ maxTurns: 120 });
    expect(input().value).toBe("120");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.any(String));
  });

  it("does not save a value outside 1-999", async () => {
    view = mount(createElement(AiRunSettingsCard));
    await flush();
    for (const value of ["0", "1000", "2.5", ""]) {
      setInputValue(input(), value);
      expect(saveButton().disabled).toBe(true);
    }
    expect(mocks.set).not.toHaveBeenCalled();
  });

  it("reports a load failure", async () => {
    mocks.get.mockRejectedValue(new Error("offline"));
    view = mount(createElement(AiRunSettingsCard));
    await flush();
    expect(view.container.querySelector('[role="alert"]')?.textContent).toContain("offline");
    expect(input().disabled).toBe(true);
  });
});
