/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { ToastStack } from "../../app/shell/ToastStack";
import { useUi } from "../../app/store";
import { mount } from "./reactTestUtils";

beforeEach(() => {
  vi.useFakeTimers();
  useUi.setState({ toasts: [] });
});
afterEach(() => {
  for (const toast of useUi.getState().toasts) useUi.getState().dismissToast(toast.id);
  vi.useRealTimers();
});

describe("ToastStack", () => {
  it("dismisses only from the close control, and caps the stack", () => {
    const view = mount(createElement(ToastStack));
    try {
      act(() => {
        for (let i = 0; i < 6; i += 1) useUi.getState().pushToast("info", `msg-${i}`);
        useUi.getState().pushToast("error", "boom");
      });
      expect(useUi.getState().toasts.map((t) => t.text)).toEqual(["msg-3", "msg-4", "msg-5", "boom"]);
      expect(view.container.textContent).toContain("boom");
      const text = [...view.container.querySelectorAll("span")].find((n) => n.textContent === "boom")!;
      act(() => text.click());
      expect(useUi.getState().toasts.length).toBe(4);
      act(() => [...view.container.querySelectorAll<HTMLButtonElement>('button[aria-label="关闭通知"]')].at(-1)!.click());
      expect(useUi.getState().toasts.map((t) => t.text)).not.toContain("boom");
    } finally {
      view.unmount();
    }
  });

  it("holds the timer while held, resumes on release, and keeps deduping", () => {
    const view = mount(createElement(ToastStack));
    try {
      act(() => {
        useUi.getState().pushToast("info", "same");
        useUi.getState().pushToast("info", "same");
      });
      expect(useUi.getState().toasts.length).toBe(1);
      const id = useUi.getState().toasts[0].id;
      act(() => { vi.advanceTimersByTime(3000); });
      act(() => useUi.getState().holdToast(id));
      act(() => { vi.advanceTimersByTime(60_000); });
      expect(useUi.getState().toasts.length).toBe(1);
      act(() => useUi.getState().releaseToast(id));
      act(() => { vi.advanceTimersByTime(1600); });
      expect(useUi.getState().toasts.length).toBe(0);
    } finally {
      view.unmount();
    }
  });
});
