/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";

import {
  SelectionActionBar,
  isCoarsePointerDevice,
} from "../../features/terminal/SelectionActionBar";
import { clickButton, mount, type MountedView } from "./reactTestUtils";

function noop() {}

describe("SelectionActionBar", () => {
  let mounted: MountedView | undefined;

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("renders copy, paste, and select-all actions with their own callbacks", () => {
    const onCopy = vi.fn();
    const onPaste = vi.fn();
    const onSelectAll = vi.fn();
    mounted = mount(
      createElement(SelectionActionBar, { x: 200, y: 300, onCopy, onPaste, onSelectAll }),
    );

    expect(mounted.container.querySelector(".nx-selection-bar")).not.toBeNull();
    clickButton(mounted.container, "复制");
    clickButton(mounted.container, "粘贴");
    clickButton(mounted.container, "全选");
    expect(onCopy).toHaveBeenCalledTimes(1);
    expect(onPaste).toHaveBeenCalledTimes(1);
    expect(onSelectAll).toHaveBeenCalledTimes(1);
  });

  it("clamps horizontally within the viewport and flips below the finger near the top", () => {
    mounted = mount(
      createElement(SelectionActionBar, { x: 4, y: 20, onCopy: noop, onPaste: noop, onSelectAll: noop }),
    );
    const bar = mounted.container.querySelector(".nx-selection-bar") as HTMLElement;
    const left = Number.parseFloat(bar.style.left);
    expect(left).toBeGreaterThanOrEqual(100);
    expect(left).toBeLessThanOrEqual(window.innerWidth - 100);
    expect(bar.style.transform).toBe("translateX(-50%)");
    expect(Number.parseFloat(bar.style.top)).toBeGreaterThan(20);
  });

  it("disables the gate with the default non-coarse matchMedia stub", () => {
    expect(isCoarsePointerDevice()).toBe(false);
  });
});
