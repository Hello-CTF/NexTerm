/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { CommandBlockPanel } from "../../features/terminal/CommandBlockPanel";
import type { CommandBlock } from "../../features/terminal/commandBlocks";
import { mount, type MountedView } from "./reactTestUtils";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

describe("CommandBlockPanel", () => {
  it("renders only visible blocks for a large command history", () => {
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const height = this.hasAttribute("data-command-block")
        ? 54
        : this.classList.contains("overflow-y-auto")
          ? 600
          : 0;
      return {
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 252,
        bottom: height,
        width: 252,
        height,
        toJSON: () => ({}),
      } as DOMRect;
    });
    vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(252);
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
      return this.classList.contains("overflow-y-auto") ? 600 : 54;
    });
    const blocks: CommandBlock[] = Array.from({ length: 1000 }, (_, index) => ({
      index: index + 1,
      command: `command ${index + 1}`,
      reliable: true,
      startedAt: 1000 + index,
      endedAt: 2000 + index,
      startLine: index,
      endLine: index,
      exitCode: 0,
    }));
    const getText = vi.fn(() => "output");
    mounted = mount(
      <CommandBlockPanel
        blocks={blocks}
        getText={getText}
        onLocate={() => undefined}
        onClear={() => undefined}
        onToast={() => undefined}
      />,
    );

    const rendered = mounted.container.querySelectorAll("[data-command-block]");
    expect(rendered.length).toBeGreaterThan(0);
    expect(rendered.length).toBeLessThan(50);
    expect(mounted.container.querySelector("[data-command-block='1']")).not.toBeNull();
    expect(mounted.container.querySelector("[data-command-block='1000']")).toBeNull();
    expect(mounted.container.querySelector(".nx-count")?.textContent).toBe("1000");
    expect(getText).not.toHaveBeenCalled();
  });
});
