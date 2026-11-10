/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { TOAST_EXIT_MS, ToastStack } from "../../app/shell/ToastStack";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
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

  it("keeps raw error text behind a details disclosure", () => {
    const view = mount(createElement(ToastStack));
    try {
      act(() => {
        useUi.getState().pushToast("error", describeError({ code: "db", message: "SQLITE_BUSY: database is locked" }));
      });
      const details = view.container.querySelector("details");
      expect(details?.textContent).toContain("详情");
      expect(details?.textContent).toContain("SQLITE_BUSY");
      expect(view.container.querySelector("span")?.textContent).not.toContain("SQLITE_BUSY");
    } finally {
      view.unmount();
    }
  });

  it("announces errors in an alert region and other kinds in a polite status region", () => {
    const view = mount(createElement(ToastStack));
    try {
      act(() => {
        useUi.getState().pushToast("info", "note");
        useUi.getState().pushToast("warning", "careful");
        useUi.getState().pushToast("error", "broken");
      });
      const status = view.container.querySelector('[role="status"]')!;
      const alert = view.container.querySelector('[role="alert"]')!;
      expect(status.getAttribute("aria-live")).toBe("polite");
      expect(status.getAttribute("aria-label")).toBe("通知");
      expect(alert.getAttribute("aria-label")).toBe("错误通知");
      expect(status.textContent).toContain("note");
      expect(status.textContent).toContain("careful");
      expect(status.textContent).not.toContain("broken");
      expect(alert.textContent).toContain("broken");
      expect(status.querySelector('[data-toast-kind="warning"]')?.className).toContain("nx-toast-warning");
    } finally {
      view.unmount();
    }
  });

  it("gives a warning longer than info but shorter than an error", () => {
    act(() => useUi.getState().pushToast("warning", "careful"));
    act(() => { vi.advanceTimersByTime(5900); });
    expect(useUi.getState().toasts.map((t) => t.text)).toEqual(["careful"]);
    act(() => { vi.advanceTimersByTime(200); });
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("freezes the countdown bar while hovered and resumes it from the remaining time", () => {
    const view = mount(createElement(ToastStack));
    try {
      act(() => useUi.getState().pushToast("info", "counting"));
      const toast = () => view.container.querySelector<HTMLElement>(".nx-toast")!;
      const bar = () => toast().querySelector<HTMLElement>(".nx-toast-progress")!;
      expect(bar().style.getPropertyValue("--nx-toast-ms")).toBe("3500ms");
      expect(bar().style.getPropertyValue("--nx-toast-left")).toBe("1");
      expect(toast().hasAttribute("data-held")).toBe(false);

      act(() => { vi.advanceTimersByTime(1000); });
      act(() => { toast().dispatchEvent(new MouseEvent("mouseover", { bubbles: true, relatedTarget: document.body })); });
      expect(toast().hasAttribute("data-held")).toBe(true);
      expect(bar().style.getPropertyValue("--nx-toast-ms")).toBe("2500ms");
      expect(Number(bar().style.getPropertyValue("--nx-toast-left"))).toBeCloseTo(2500 / 3500);

      act(() => { vi.advanceTimersByTime(60_000); });
      expect(view.container.textContent).toContain("counting");

      act(() => { toast().dispatchEvent(new MouseEvent("mouseout", { bubbles: true, relatedTarget: document.body })); });
      expect(toast().hasAttribute("data-held")).toBe(false);
      act(() => { vi.advanceTimersByTime(2400); });
      expect(useUi.getState().toasts.length).toBe(1);
      act(() => { vi.advanceTimersByTime(200); });
      expect(useUi.getState().toasts.length).toBe(0);
    } finally {
      view.unmount();
    }
  });

  it("keeps a dismissed toast in a leaving state, then removes it on a timer", () => {
    const view = mount(createElement(ToastStack));
    try {
      act(() => useUi.getState().pushToast("success", "saved"));
      act(() => view.container.querySelector<HTMLButtonElement>('button[aria-label="关闭通知"]')!.click());
      expect(useUi.getState().toasts).toEqual([]);
      const leaving = view.container.querySelector<HTMLElement>(".nx-toast");
      expect(leaving?.classList.contains("is-leaving")).toBe(true);
      expect(leaving?.getAttribute("aria-hidden")).toBe("true");
      expect(leaving?.querySelector(".nx-toast-progress")).toBeNull();

      act(() => { vi.advanceTimersByTime(TOAST_EXIT_MS - 1); });
      expect(view.container.querySelector(".nx-toast")).not.toBeNull();
      act(() => { vi.advanceTimersByTime(1); });
      expect(view.container.querySelector(".nx-toast")).toBeNull();
    } finally {
      view.unmount();
    }
  });

  it("removes a dismissed toast at once under reduced motion", () => {
    const original = window.matchMedia;
    window.matchMedia = ((query: string) => ({ matches: query.includes("reduce"), media: query }) as MediaQueryList);
    const view = mount(createElement(ToastStack));
    try {
      act(() => useUi.getState().pushToast("info", "brief"));
      act(() => view.container.querySelector<HTMLButtonElement>('button[aria-label="关闭通知"]')!.click());
      expect(view.container.querySelector(".nx-toast")).toBeNull();
    } finally {
      view.unmount();
      window.matchMedia = original;
    }
  });

  it("renders extra stack content above the toasts", () => {
    const view = mount(createElement(ToastStack, null, createElement("p", { id: "extra" }, "running")));
    try {
      expect(view.container.querySelector(".nx-toasts")?.firstElementChild?.id).toBe("extra");
    } finally {
      view.unmount();
    }
  });
});
