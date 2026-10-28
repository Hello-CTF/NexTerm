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
  it("marks only a live terminal that is explicitly not durable", () => {
    const icon = mark(terminal({ durable: false }));
    expect(icon?.getAttribute("aria-label")).toBe("临时终端：关闭标签页或断开连接后进程会结束");
    expect(icon?.querySelector("svg")?.getAttribute("width")).toBe("12");
    expect(mark(terminal({ durable: false, exited: true }))).toBeNull();
    expect(mark(terminal({ durable: false, dead: true }))).toBeNull();
  });

  it("leaves durable, unknown and non-terminal tabs unmarked", () => {
    expect(mark(terminal({ durable: true }))).toBeNull();
    expect(mark(terminal())).toBeNull();
    expect(mark(terminal({ regularReason: "持久组件不可用：创建持久终端失败，已改用临时终端" }))).toBeNull();
    expect(mark({ id: "f1", kind: "files", title: "文件", closable: true, durable: false })).toBeNull();
  });

  it("keeps the full regular reason in the tab tooltip", () => {
    expect(tabTooltip(terminal({ regularReason: "持久组件不可用：创建持久终端失败，已改用临时终端" }), false)).toBe(
      "终端 1（临时终端：持久组件不可用：创建持久终端失败，已改用临时终端）",
    );
    expect(tabTooltip(terminal({ durable: true }), false)).toContain("持久终端");
    expect(tabTooltip(terminal({ durable: false }), false)).toContain("临时终端");
    expect(tabTooltip(terminal(), false)).toBe("终端 1");
  });

  it("prefers the pane's live note and names a dismissed failure", () => {
    const tab = terminal({ regularReason: "持久组件准备中" });
    expect(tabTooltip(tab, false, { note: "临时终端：持久终端已就绪，新建的终端将是持久终端" })).toBe(
      "终端 1（临时终端：持久终端已就绪，新建的终端将是持久终端）",
    );
    const failed = tabTooltip(tab, false, { note: "临时终端：持久组件不可用：x", failureMark: "本标签页是临时终端（持久组件不可用：x），断开后进程会结束。" });
    expect(failed).toContain("本标签页是临时终端（持久组件不可用：x）");
    expect(failed).not.toContain("持久组件准备中");
  });
});
