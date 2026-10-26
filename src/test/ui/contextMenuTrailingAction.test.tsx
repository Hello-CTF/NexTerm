/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ContextMenu, type MenuItem } from "../../ui/ContextMenu";
import { mount, type MountedView } from "../features/reactTestUtils";

const realMatchMedia = window.matchMedia;
let mounted: MountedView | undefined;

beforeEach(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  window.matchMedia = realMatchMedia;
});

function clickTrailing(keepOpen: boolean) {
  const onClose = vi.fn();
  const onSelect = vi.fn();
  const items: MenuItem[] = [
    { kind: "item", label: "Workspace", trailingAction: { label: "Close Workspace", icon: "x", keepOpen, onSelect } },
  ];
  mounted = mount(createElement(ContextMenu, { state: { x: 0, y: 0, items }, onClose }));
  const button = mounted.container.querySelector<HTMLButtonElement>("[aria-label='Close Workspace']");
  if (!button) throw new Error("trailing action not found");
  act(() => button.click());
  return { onClose, onSelect };
}

describe("context menu trailing action", () => {
  it("keeps the menu open for a keepOpen action", () => {
    const { onClose, onSelect } = clickTrailing(true);
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("closes the menu for an ordinary action", () => {
    const { onClose, onSelect } = clickTrailing(false);
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
