/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";
import { TabPersistenceMark, tabTooltip } from "../../app/workspace/tabStrip";
import type { AppTab } from "../../app/store";

const mounted: MountedView[] = [];

afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount();
});

function terminal(patch: Partial<AppTab> = {}): AppTab {
  return { id: "t1", kind: "terminal", title: "终端 1", sessionId: "s1", closable: true, ...patch };
}

function mark(tab: AppTab): Element | null {
  const view = mount(createElement(TabPersistenceMark, { tab }));
  mounted.push(view);
  return view.container.querySelector('[role="img"]');
}

describe("tab persistence mark", () => {
  it("marks a durable terminal tab with an accessible persistence icon", () => {
    const icon = mark(terminal({ durable: true }));
    expect(icon?.getAttribute("aria-label")).toBe("持久终端");
    expect(icon?.querySelector("svg")?.getAttribute("width")).toBe("12");
  });

  it("leaves regular terminal tabs unmarked, with or without a reason", () => {
    expect(mark(terminal({ durable: false }))).toBeNull();
    expect(mark(terminal({ regularReason: "持久组件不可用：创建持久终端失败，已改用普通终端" }))).toBeNull();
    expect(mark({ id: "f1", kind: "files", title: "文件", closable: true })).toBeNull();
  });

  it("keeps the full regular reason in the tab tooltip", () => {
    expect(tabTooltip(terminal({ regularReason: "持久组件不可用：创建持久终端失败，已改用普通终端" }), false)).toBe(
      "终端 1（普通终端：持久组件不可用：创建持久终端失败，已改用普通终端）",
    );
    expect(tabTooltip(terminal({ durable: true }), false)).toContain("持久终端");
  });
});
