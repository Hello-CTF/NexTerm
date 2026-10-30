/** @vitest-environment jsdom */
import { lazy } from "react";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import { Lazy } from "../ui/LazyBoundary";
import { mount } from "./features/reactTestUtils";

const Pending = lazy(() => new Promise<never>(() => undefined));

describe("Lazy fallback", () => {
  it("renders the given element, which keeps the slot's size while the chunk loads", () => {
    const fallback = createElement("div", { "data-testid": "slot", style: { width: 280 } });
    const view = mount(createElement(Lazy, { fallback, children: createElement(Pending) }));
    try {
      const slot = view.container.querySelector<HTMLElement>('[data-testid="slot"]');
      expect(slot?.style.width).toBe("280px");
    } finally {
      view.unmount();
    }
  });
});
