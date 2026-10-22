/** @vitest-environment jsdom */

import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ create: vi.fn() }));

vi.mock("../../ipc/hostShareApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/hostShareApi")>();
  return { ...actual, createHostShareConnection: mocks.create };
});

vi.mock("../../features/sharing/PublicShareTerminal", () => ({
  PublicShareTerminal: () => null,
}));

import { HostShareTerminalApp } from "../../features/sharing/HostShareTerminalApp";

let mounted: MountedView | undefined;

beforeEach(() => {
  mocks.create.mockReturnValue({ sendInput: vi.fn(), close: vi.fn() });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

describe("HostShareTerminalApp safe area", () => {
  it("uses the fullscreen safe-bottom surface for its fixed terminal", () => {
    mounted = mount(createElement(HostShareTerminalApp, {
      deviceId: "device-1",
      deviceName: "shared-host",
      ownerUsername: "owner",
      onClose: vi.fn(),
    }));

    const dialog = mounted.container.querySelector(".nx-fullscreen-safe-bottom");
    expect(dialog).not.toBeNull();
    expect(dialog?.classList.contains("fixed")).toBe(true);
    expect(dialog?.querySelector("main")).not.toBeNull();
  });
});
