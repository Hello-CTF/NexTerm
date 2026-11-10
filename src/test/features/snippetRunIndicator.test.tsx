/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";
import { SnippetRunIndicator } from "../../features/explorer/SnippetRunIndicator";
import { beginSnippetRun } from "../../features/explorer/snippetRuns";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("snippet run indicator", () => {
  it("shows the step being waited for and cancels the run", () => {
    const cancel = vi.fn();
    mounted = mount(createElement(SnippetRunIndicator));
    expect(mounted.container.querySelector('[data-testid="snippet-run-indicator"]')).toBeNull();
    let run!: ReturnType<typeof beginSnippetRun>;
    act(() => {
      run = beginSnippetRun({ name: "login", total: 3, cancel });
      run.update({ step: 2, waitingFor: "Password:" });
    });
    const indicator = mounted.container.querySelector('[data-testid="snippet-run-indicator"]');
    expect(indicator?.textContent).toContain("第 2 / 3 步");
    expect(indicator?.textContent).toContain("Password:");
    click(mounted.container.querySelector('[aria-label="取消执行「login」"]')!);
    expect(cancel).toHaveBeenCalledTimes(1);
    act(() => run.end());
    expect(mounted.container.querySelector('[data-testid="snippet-run-indicator"]')).toBeNull();
  });
});
