/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CommandBlockPanel } from "../../features/terminal/CommandBlockPanel";
import type { CommandBlock } from "../../features/terminal/commandBlocks";
import { mount, type MountedView } from "./reactTestUtils";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

describe("CommandBlockPanel", () => {
  it("renders only visible blocks for a large command history", () => {
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const height = this.hasAttribute("data-command-block")
        ? 54
        : this.classList.contains("overflow-y-auto")
          ? 600
          : 0;
      return {
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 252,
        bottom: height,
        width: 252,
        height,
        toJSON: () => ({}),
      } as DOMRect;
    });
    vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(252);
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
      return this.classList.contains("overflow-y-auto") ? 600 : 54;
    });
    const blocks: CommandBlock[] = Array.from({ length: 1000 }, (_, index) => ({
      index: index + 1,
      command: `command ${index + 1}`,
      reliable: true,
      startedAt: 1000 + index,
      endedAt: 2000 + index,
      startLine: index,
      endLine: index,
      exitCode: 0,
    }));
    const getText = vi.fn(() => "output");
    mounted = mount(
      <CommandBlockPanel
        blocks={blocks}
        getText={getText}
        onLocate={() => undefined}
        onClear={() => undefined}
        onToast={() => undefined}
      />,
    );

    const rendered = mounted.container.querySelectorAll("[data-command-block]");
    expect(rendered.length).toBeGreaterThan(0);
    expect(rendered.length).toBeLessThan(50);
    expect(mounted.container.querySelector("[data-command-block='1']")).not.toBeNull();
    expect(mounted.container.querySelector("[data-command-block='1000']")).toBeNull();
    expect(mounted.container.querySelector(".nx-count")?.textContent).toBe("1000");
    expect(getText).not.toHaveBeenCalled();
  });
});

describe("CommandBlockPanel actions", () => {
  const mk = (index: number, command: string, ended: boolean): CommandBlock => ({
    index, command, reliable: true, startedAt: 1000, endedAt: ended ? 2000 : null, startLine: 0, endLine: 1, exitCode: ended ? 0 : null,
  });
  const base = { getText: () => "out", onLocate: () => undefined, onClear: () => undefined, onToast: () => undefined };
  beforeEach(() => {
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const height = this.hasAttribute("data-command-block") ? 54 : this.classList.contains("overflow-y-auto") ? 600 : 0;
      return { x: 0, y: 0, top: 0, left: 0, right: 252, bottom: height, width: 252, height, toJSON: () => ({}) } as DOMRect;
    });
    vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(252);
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
      return this.classList.contains("overflow-y-auto") ? 600 : 54;
    });
  });
  const titles = (c: HTMLElement) => Array.from(c.querySelectorAll("button")).map((b) => b.getAttribute("title"));

  it("shows no extra buttons when the handlers are not passed", () => {
    mounted = mount(<CommandBlockPanel {...base} blocks={[mk(1, "ls", true)]} />);
    const t = titles(mounted.container);
    expect(t).not.toContain("重新运行");
    expect(t).not.toContain("保存为片段");
    expect(t).not.toContain("让 AI 解释这段输出");
  });

  it("calls the handlers of a finished block", () => {
    const onRerun = vi.fn();
    const onSaveAsSnippet = vi.fn();
    const onExplain = vi.fn();
    const done = mk(1, "ls", true);
    mounted = mount(
      <CommandBlockPanel {...base} blocks={[done]} onRerun={onRerun} onSaveAsSnippet={onSaveAsSnippet} onExplain={onExplain} />,
    );
    const row = mounted.container.querySelector("[data-command-block='1']")!;
    (row.querySelector('button[title="重新运行"]') as HTMLElement).click();
    (row.querySelector('button[title="保存为片段"]') as HTMLElement).click();
    (row.querySelector('button[title="让 AI 解释这段输出"]') as HTMLElement).click();
    expect(onRerun).toHaveBeenCalledWith(done);
    expect(onSaveAsSnippet).toHaveBeenCalledWith(done);
    expect(onExplain).toHaveBeenCalledWith(done, "out");
  });

  it("disables every rerun button while any command is still running", () => {
    const onRerun = vi.fn();
    mounted = mount(
      <CommandBlockPanel {...base} blocks={[mk(1, "ls", true), mk(2, "pwd", true), mk(3, "sleep 9", false)]} onRerun={onRerun} />,
    );
    const buttons = Array.from(mounted.container.querySelectorAll<HTMLButtonElement>('button[aria-label="重新运行"]'));
    expect(buttons.length).toBeGreaterThan(0);
    for (const button of buttons) {
      expect(button.disabled).toBe(true);
      expect(button.title).toBe("终端里还有命令在运行，结束后才能重新运行");
      button.click();
    }
    expect(onRerun).not.toHaveBeenCalled();
  });

  it("tells the user instead of explaining when the output scrolled out", () => {
    const onExplain = vi.fn();
    const onToast = vi.fn();
    mounted = mount(
      <CommandBlockPanel {...base} getText={() => ""} onToast={onToast} blocks={[mk(1, "ls", true)]} onExplain={onExplain} />,
    );
    (mounted.container.querySelector('button[title="让 AI 解释这段输出"]') as HTMLElement).click();
    expect(onExplain).not.toHaveBeenCalled();
    expect(onToast).toHaveBeenCalledWith("info", expect.any(String));
  });
});
