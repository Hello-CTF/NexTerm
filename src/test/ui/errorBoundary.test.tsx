/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { click, mount, type MountedView } from "../features/reactTestUtils";
import { ErrorBoundary } from "../../ui/ErrorBoundary";

function Bomb(): never {
  throw new Error("boom");
}

describe("ErrorBoundary", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    document.body.replaceChildren();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    mounted?.unmount();
    mounted = undefined;
  });

  it("renders children when nothing throws", () => {
    mounted = mount(
      <ErrorBoundary>
        <div>正常内容</div>
      </ErrorBoundary>,
    );
    expect(mounted.container.textContent).toBe("正常内容");
    expect(mounted.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("shows the error with retry and close actions when a child crashes", () => {
    const onClose = vi.fn();
    mounted = mount(
      <ErrorBoundary onClose={onClose}>
        <Bomb />
      </ErrorBoundary>,
    );

    const alert = mounted.container.querySelector<HTMLElement>('[role="alert"]');
    expect(alert).not.toBeNull();
    expect(alert?.textContent).toContain("面板发生错误");
    expect(alert?.textContent).toContain("boom");

    const buttons = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="alert"] button')];
    expect(buttons.map((b) => b.textContent)).toEqual(["重试", "关闭标签"]);

    click(buttons[1]);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("hides the close action when no onClose is given", () => {
    mounted = mount(
      <ErrorBoundary>
        <Bomb />
      </ErrorBoundary>,
    );
    const buttons = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="alert"] button')];
    expect(buttons.map((b) => b.textContent)).toEqual(["重试"]);
  });

  it("remounts the children on retry", () => {
    let broken = true;
    function Flaky() {
      if (broken) throw new Error("渲染崩溃");
      return <div>恢复内容</div>;
    }

    mounted = mount(
      <ErrorBoundary>
        <Flaky />
      </ErrorBoundary>,
    );
    expect(mounted.container.textContent).toContain("渲染崩溃");
    expect(mounted.container.textContent).not.toContain("恢复内容");

    broken = false;
    click(mounted.container.querySelector<HTMLButtonElement>('[role="alert"] button')!);
    expect(mounted.container.textContent).toContain("恢复内容");
    expect(mounted.container.querySelector('[role="alert"]')).toBeNull();
  });
});
