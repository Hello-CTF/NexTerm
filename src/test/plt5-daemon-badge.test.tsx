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
    expect(badgeText(true, "ssh")).toContain("守护进程");
  });

  it("shows the direct-connection badge for volatile tabs", () => {
    expect(badgeText(false, "ssh")).toContain("直接连接");
  });

  it("hides for container and line-mode sessions", () => {
    expect(badgeText(true, "docker")).toBe("");
    expect(badgeText(true, "winrm")).toBe("");
  });
});
