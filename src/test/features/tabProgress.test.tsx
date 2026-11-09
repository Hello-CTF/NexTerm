/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";
import { TabProgress } from "../../app/workspace/tabStrip";
import { applyAwareness, resetAwarenessStore } from "../../app/sessionAwarenessStore";
import { attentionDetail } from "../../features/inbox/inbox";
import type { TerminalAttentionEvent } from "../../ipc/events";

let version = 0;
let view: MountedView | undefined;

beforeEach(() => {
  resetAwarenessStore();
  view = mount(createElement(TabProgress, { tabId: "T1" }));
});

afterEach(() => {
  view?.unmount();
  view = undefined;
  resetAwarenessStore();
});

function report(patch: Partial<TerminalAttentionEvent>): void {
  version += 1;
  act(() => {
    applyAwareness([{ tabId: "T1", state: "running", confidence: "signal", source: "helper", version, ...patch }]);
  });
}

function bar(): Element | null {
  return view?.container.querySelector('[role="progressbar"]') ?? null;
}

describe("tab progress", () => {
  it("shows a program's progress value", () => {
    expect(bar()).toBeNull();
    report({ progress: { state: "normal", value: 40 } });
    expect(bar()?.getAttribute("aria-valuenow")).toBe("40");
    expect(bar()?.getAttribute("aria-label")).toBe("进度 40%");
    expect(bar()?.getAttribute("data-progress")).toBe("normal");
  });

  it("names error and paused states and keeps their value", () => {
    report({ progress: { state: "error", value: 70 } });
    expect(bar()?.getAttribute("data-progress")).toBe("error");
    expect(bar()?.getAttribute("aria-label")).toBe("进度出错（70%）");
    report({ progress: { state: "paused" } });
    expect(bar()?.getAttribute("aria-label")).toBe("进度已暂停");
    expect(bar()?.hasAttribute("aria-valuenow")).toBe(false);
  });

  it("turns only while the progress is unknown and only when motion is allowed", () => {
    report({ progress: { state: "normal", value: 10 } });
    expect(bar()?.querySelector("svg")?.getAttribute("class") ?? "").not.toContain("animate-spin");
    report({ progress: { state: "indeterminate", value: 50 } });
    expect(bar()?.hasAttribute("aria-valuenow")).toBe(false);
    expect(bar()?.getAttribute("aria-label")).toBe("进行中，进度未知");
    expect(bar()?.querySelector("svg")?.getAttribute("class")).toBe("motion-safe:animate-spin");
  });

  it("clamps the value and ignores a progress state it does not know", () => {
    report({ progress: { state: "normal", value: 250 } });
    expect(bar()?.getAttribute("aria-valuenow")).toBe("100");
    report({ progress: { state: "sparkle", value: 5 } });
    expect(bar()).toBeNull();
  });

  it("goes away when the backend clears it", () => {
    report({ progress: { state: "normal", value: 90 } });
    expect(bar()).not.toBeNull();
    report({ state: "just_finished", exitCode: 0 });
    expect(bar()).toBeNull();
  });

  it("explains a progress error in the inbox", () => {
    expect(attentionDetail({ state: "needs_me", reason: "progress_error", seen: false, notable: false, version: 1 })).toBe(
      "程序报告进度出错",
    );
  });
});
