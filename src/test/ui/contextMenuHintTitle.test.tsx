/** @vitest-environment jsdom */

import { createElement } from "react";
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

describe("context menu hint", () => {
  it("exposes the full hint as a title so a truncated hint stays readable", () => {
    const hint = "~/.ssh/config 或 Termius";
    const items: MenuItem[] = [
      { kind: "item", label: "导入 SSH 配置", hint },
      { kind: "item", label: "无提示" },
    ];
    mounted = mount(createElement(ContextMenu, { state: { x: 0, y: 0, items }, onClose: vi.fn() }));
    const hints = mounted.container.querySelectorAll<HTMLElement>(".nx-menu-hint");
    expect(hints).toHaveLength(1);
    expect(hints[0].textContent).toBe(hint);
    expect(hints[0].title).toBe(hint);
  });
});
