/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, mount, type MountedView } from "./features/reactTestUtils";
import { Cwd, shortenCwd } from "../features/terminal/Cwd";

const mounted: MountedView[] = [];

afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount();
  vi.restoreAllMocks();
});

describe("shortenCwd", () => {
  it("keeps short paths intact", () => {
    expect(shortenCwd("/etc/nginx")).toBe("/etc/nginx");
  });

  it("keeps the last two segments of long paths", () => {
    expect(shortenCwd("/very/long/path/to/some/project")).toBe("…/some/project");
  });

  it("handles root", () => {
    expect(shortenCwd("/")).toBe("/");
  });
});

describe("Cwd", () => {
  it("renders nothing without a cwd", () => {
    const view = mount(createElement(Cwd, { cwd: null }));
    mounted.push(view);
    expect(view.container.querySelector("button")).toBeNull();
  });

  it("renders the reported cwd and copies the full path on click", () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    const view = mount(createElement(Cwd, { cwd: "/srv/www/app" }));
    mounted.push(view);
    const button = view.container.querySelector("button");
    expect(button?.textContent).toBe("/srv/www/app");
    expect(button?.getAttribute("title")).toContain("/srv/www/app");
    click(button!);
    expect(writeText).toHaveBeenCalledWith("/srv/www/app");
  });
});
