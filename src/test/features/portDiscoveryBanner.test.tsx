/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  snapshot: { fresh: [] as { address: string; port: number }[], all: [] as { address: string; port: number }[] },
  dismiss: vi.fn(),
  forwardAndOpen: vi.fn(),
}));

vi.mock("../../features/preview/portDiscovery", () => ({
  usePortDiscovery: () => mocks.snapshot,
  dismissDiscoveredPorts: mocks.dismiss,
}));
vi.mock("../../features/preview/forwardAndOpen", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../features/preview/forwardAndOpen")>()),
  forwardAndOpen: mocks.forwardAndOpen,
}));

import { PortDiscoveryBanner } from "../../features/preview/PortDiscoveryBanner";

let view: MountedView | null = null;

function render() {
  view = mount(createElement(PortDiscoveryBanner, { sessionId: "s1" }));
}

const banner = () => document.querySelector('[data-testid="port-discovery-banner"]');
const portButtons = () => [...document.querySelectorAll<HTMLButtonElement>("button[title^='建立临时转发']")];

beforeEach(() => {
  mocks.snapshot = { fresh: [], all: [] };
  mocks.dismiss.mockReset();
  mocks.forwardAndOpen.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("PortDiscoveryBanner", () => {
  it("renders nothing when no new port was found", () => {
    render();
    expect(banner()).toBeNull();
  });

  it("lists each new port once and forwards through loopback for a wildcard bind", () => {
    mocks.snapshot = {
      fresh: [
        { address: "0.0.0.0", port: 3000 },
        { address: "::", port: 3000 },
      ],
      all: [{ address: "0.0.0.0", port: 3000 }],
    };
    render();
    const buttons = portButtons();
    expect(buttons).toHaveLength(1);
    expect(buttons[0].textContent).toBe("3000，转发并打开");
    act(() => buttons[0].click());
    expect(mocks.forwardAndOpen).toHaveBeenCalledWith({ sessionId: "s1", host: "127.0.0.1", port: 3000 });
  });

  it("dials a non-loopback bind address directly", () => {
    mocks.snapshot = { fresh: [{ address: "10.0.0.5", port: 8080 }], all: [{ address: "10.0.0.5", port: 8080 }] };
    render();
    act(() => portButtons()[0].click());
    expect(mocks.forwardAndOpen).toHaveBeenCalledWith({ sessionId: "s1", host: "10.0.0.5", port: 8080 });
  });

  it("shows at most three ports and counts the rest", () => {
    mocks.snapshot = {
      fresh: [3001, 3002, 3003, 3004, 3005].map((port) => ({ address: "127.0.0.1", port })),
      all: [],
    };
    render();
    expect(portButtons().map((b) => b.textContent)).toEqual(["3001，转发并打开", "3002，转发并打开", "3003，转发并打开"]);
    expect(banner()?.textContent).toContain("另有 2 个");
  });

  it("dismisses every listed port, including the hidden ones", () => {
    mocks.snapshot = {
      fresh: [3001, 3002, 3003, 3004].map((port) => ({ address: "127.0.0.1", port })),
      all: [],
    };
    render();
    act(() => document.querySelector<HTMLButtonElement>('[aria-label="收起端口提示"]')!.click());
    expect(mocks.dismiss).toHaveBeenCalledWith("s1", [3001, 3002, 3003, 3004]);
  });
});
