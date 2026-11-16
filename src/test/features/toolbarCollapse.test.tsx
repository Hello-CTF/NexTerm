/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement, useRef } from "react";
import { mount, type MountedView } from "./reactTestUtils";
import { useCollapseOnOverflow } from "../../features/terminal/toolbarCollapse";

// jsdom has no layout: the probe's toolbar reports the widths the test sets.
const size = { client: 600, scroll: 600 };
let resize: (() => void) | null = null;
const measured = new WeakSet<Element>();

class FakeResizeObserver {
  constructor(callback: () => void) {
    resize = callback;
  }
  observe() {}
  disconnect() {
    resize = null;
  }
}

function Probe({ label }: { label: string }) {
  const ref = useRef<HTMLDivElement | null>(null);
  const collapsed = useCollapseOnOverflow(ref);
  return createElement(
    "div",
    {
      ref: (element: HTMLDivElement | null) => {
        if (element && !measured.has(element)) {
          measured.add(element);
          // A collapsed toolbar hides its buttons, so its content fits again.
          const fits = () => element.getAttribute("data-collapsed") === "true";
          Object.defineProperty(element, "clientWidth", { configurable: true, get: () => size.client });
          Object.defineProperty(element, "scrollWidth", { configurable: true, get: () => (fits() ? Math.min(size.scroll, size.client) : size.scroll) });
        }
        ref.current = element;
      },
      "data-collapsed": String(collapsed),
    },
    label,
  );
}

let view: MountedView | undefined;
afterEach(() => {
  view?.unmount();
  view = undefined;
  vi.unstubAllGlobals();
});

const state = () => view!.container.querySelector("[data-collapsed]")!.getAttribute("data-collapsed");

describe("toolbar collapse on overflow", () => {
  it("collapses when the content outgrows the toolbar and expands once the toolbar can hold it again", () => {
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    size.client = 600;
    size.scroll = 600;
    view = mount(createElement(Probe, { label: "a" }));
    expect(state()).toBe("false");

    size.client = 400;
    act(() => resize?.());
    expect(state()).toBe("true");

    size.client = 550;
    act(() => resize?.());
    expect(state()).toBe("true");

    size.client = 620;
    act(() => resize?.());
    expect(state()).toBe("false");
  });

  it("collapses after a render that made the content wider without a resize", () => {
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    size.client = 600;
    size.scroll = 500;
    view = mount(createElement(Probe, { label: "short" }));
    expect(state()).toBe("false");
    size.scroll = 700;
    act(() => view!.root.render(createElement(Probe, { label: "a much longer title" })));
    expect(state()).toBe("true");
  });
});
