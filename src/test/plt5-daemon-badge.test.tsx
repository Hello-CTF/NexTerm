/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "./features/reactTestUtils";
import { Daemon } from "../features/terminal/Daemon";

const mounted: MountedView[] = [];

afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount();
});

function badgeText(durable: boolean, sessionKind?: string): string {
  const view = mount(createElement(Daemon, { durable, sessionKind }));
  mounted.push(view);
  return view.container.textContent ?? "";
}

describe("Daemon", () => {
  it("shows the daemon badge for durable tabs", () => {
    expect(badgeText(true, "ssh")).toContain("持久终端");
  });

  it("shows the direct-connection badge for volatile tabs", () => {
    expect(badgeText(false, "ssh")).toContain("普通终端");
  });

  it("hides for container and line-mode sessions", () => {
    expect(badgeText(true, "docker")).toBe("");
    expect(badgeText(true, "winrm")).toBe("");
  });
});

describe("Daemon helper states", () => {
  function render(props: Parameters<typeof Daemon>[0]) {
    const view = mount(createElement(Daemon, props));
    mounted.push(view);
    return view.container;
  }

  it("shows installation, attention, and readiness on regular SSH tabs", () => {
    expect(render({ durable: false, sessionKind: "ssh", helper: { state: "installing", stage: "上传持久终端组件" } }).textContent).toContain("正在启用持久终端");
    expect(render({ durable: false, sessionKind: "ssh", helper: { state: "attention" } }).textContent).toContain("持久终端需处理");
    expect(render({ durable: false, sessionKind: "ssh", helper: { state: "ready" } }).textContent).toContain("持久已就绪");
  });

  it("keeps durable tabs labelled as daemon terminals regardless of helper state", () => {
    expect(render({ durable: true, sessionKind: "ssh", helper: { state: "ready" } }).textContent).toContain("持久终端");
  });

  it("opens the helper from the badge on SSH tabs", () => {
    let opened = 0;
    const container = render({ durable: false, sessionKind: "ssh", onOpenHelper: () => { opened += 1; } });
    container.querySelector("button")?.click();
    expect(opened).toBe(1);
  });
});
